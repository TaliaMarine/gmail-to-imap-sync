package jobs

import (
	"context"
	"hash/fnv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/metrics"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/progress"
)

// Maintain runs the periodic duties until ctx ends: enqueue scheduled
// sessions, abandon sessions of dead workers, refresh gauges and prune old
// history. It is safe to run in several processes: every step is idempotent
// (the unique index makes a duplicate enqueue a no-op).
func (r *Runner) Maintain(ctx context.Context) {
	r.tick(ctx)
	t := time.NewTicker(r.o.ScheduleEvery)
	defer t.Stop()
	var lastPrune time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tick(ctx)
			if r.o.Now().Sub(lastPrune) > 6*time.Hour {
				lastPrune = r.o.Now()
				r.prune(ctx)
			}
		}
	}
}

func (r *Runner) tick(ctx context.Context) {
	r.sweepExpired(ctx)
	r.schedule(ctx)
	r.refreshGauges(ctx)
}

// sweepExpired marks sessions whose lease lapsed as abandoned and requeues
// them (recovery).
func (r *Runner) sweepExpired(ctx context.Context) {
	rows, err := r.o.DB.AbandonExpiredSessions(ctx)
	if err != nil {
		if ctx.Err() == nil {
			r.o.Logger.Error("sweep expired sessions", "error", err)
		}
		return
	}
	for _, s := range rows {
		r.o.Logger.Warn("session abandoned: its worker stopped responding", "session", s.ID, "account", s.AccountID)
		_ = r.o.DB.InsertEvent(ctx, store.InsertEventParams{SessionID: s.ID, EventType: "abandoned", Severity: "warning",
			Message: "The worker running this session stopped responding; the work is resumed in a new session", Metadata: []byte("{}")})
		r.publish(progress.Event{Kind: progress.KindFinished, AccountID: s.AccountID.String(), SessionID: s.ID.String()})
		r.requeue(ctx, s)
	}
}

// schedule enqueues an incremental session for each enabled account that is
// due. A fixed per-account offset (up to 10 % of the interval) spreads
// accounts out instead of starting them all together.
func (r *Runner) schedule(ctx context.Context) {
	rows, err := r.o.DB.ListScheduledAccounts(ctx)
	if err != nil {
		if ctx.Err() == nil {
			r.o.Logger.Error("list scheduled accounts", "error", err)
		}
		return
	}
	now := r.o.Now()
	for _, row := range rows {
		interval := time.Duration(row.ScheduleIntervalSeconds) * time.Second
		if !due(now, row.LastScheduled, interval, row.AccountID) {
			continue
		}
		trigger, syncType := "scheduled", "incremental"
		if st, err := r.o.DB.GetSyncState(ctx, row.AccountID); err != nil || !st.InitialSyncCompleted {
			trigger, syncType = "initial", "initial"
		}
		if _, err := r.enqueue(ctx, row.AccountID, trigger, syncType, nil, nil); err != nil {
			r.o.Logger.Error("enqueue scheduled session", "account", row.AccountID, "error", err)
		}
	}
}

// due reports whether an account whose last run started at last is due again.
func due(now time.Time, last pgtype.Timestamptz, interval time.Duration, account uuid.UUID) bool {
	if !last.Valid {
		return true
	}
	h := fnv.New32a()
	_, _ = h.Write(account[:])
	offset := time.Duration(float64(interval) * 0.1 * float64(h.Sum32()%1000) / 1000)
	return now.Sub(last.Time) >= interval+offset
}

func (r *Runner) refreshGauges(ctx context.Context) {
	if g, err := r.o.DB.SessionGauges(ctx); err == nil {
		metrics.QueueDepth.Set(float64(g.Queued))
	}
	if rows, err := r.o.DB.CountFailures(ctx); err == nil {
		var pending int64
		for _, row := range rows {
			if row.Status == "pending" {
				pending += row.N
			}
		}
		metrics.PendingFailures.Set(float64(pending))
	}
	if rows, err := r.o.DB.CountAccountsByStatus(ctx); err == nil {
		var n int64
		for _, row := range rows {
			if row.Status == "needs_reauth" {
				n = row.N
			}
		}
		metrics.NeedsReauth.Set(float64(n))
	}
	if rows, err := r.o.DB.LastSuccessByAccount(ctx); err == nil {
		for _, row := range rows {
			metrics.LastSuccess.WithLabelValues(row.AccountID.String()).Set(float64(row.FinishedAt.Time.Unix()))
		}
	}
}

// prune deletes sessions and events past their retention.
func (r *Runner) prune(ctx context.Context) {
	s, err := r.o.DB.GetAppSettings(ctx)
	if err != nil {
		return
	}
	now := r.o.Now()
	if n, err := r.o.DB.PruneEvents(ctx, pgtype.Timestamptz{Time: now.AddDate(0, 0, -int(s.EventRetentionDays)), Valid: true}); err == nil && n > 0 {
		r.o.Logger.Info("pruned session events", "rows", n)
	}
	if n, err := r.o.DB.PruneSessions(ctx, pgtype.Timestamptz{Time: now.AddDate(0, 0, -int(s.SessionRetentionDays)), Valid: true}); err == nil && n > 0 {
		r.o.Logger.Info("pruned sessions", "rows", n)
	}
}
