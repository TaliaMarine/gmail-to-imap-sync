// Package jobs runs synchronisation sessions: it claims queued sessions
// with a lease, executes them with the engine, enqueues scheduled runs,
// recovers sessions of dead workers and prunes old history.
//
// There is no separate job table: a session row in status "queued" is the
// job (FULL_SPECS.md §12). Several processes can run a Runner against one
// database; FOR UPDATE SKIP LOCKED hands each session to exactly one, and a
// partial unique index allows only one queued-or-running session per account.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/clients"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/engine"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/gmail"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/imapdest"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/metrics"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/progress"
)

// Resources are what one session needs to run.
type Resources struct {
	Gmail gmail.Source
	Dest  engine.Dest
	// Close releases the resources after the session.
	Close func()
}

// Builder creates the Resources for a claimed session. The production
// implementation (NewBuilder) reads credentials from the database; tests use
// fakes.
type Builder interface {
	Build(ctx context.Context, sess store.SyncSession) (Resources, error)
}

// Options configure a Runner.
type Options struct {
	DB      *db.DB
	Builder Builder
	Logger  *slog.Logger
	// Hub receives progress events for in-process subscribers; may be nil.
	Hub *progress.Hub
	// Notifier gets told about sessions needing attention; may be nil.
	Notifier *Notifier
	// WorkerID identifies this process in leases; default hostname-pid.
	WorkerID string
	// MaxConcurrent caps simultaneously running sessions (accounts); default 2.
	MaxConcurrent int
	// PollEvery is how often the queue is polled when nothing wakes it; default 3s.
	PollEvery time.Duration
	// ScheduleEvery is the scheduler / sweeper tick; default 30s.
	ScheduleEvery time.Duration
	// LeaseSeconds is the session lease length; default 60.
	LeaseSeconds int32
	// HeartbeatEvery overrides the engine heartbeat (tests).
	HeartbeatEvery time.Duration
	// Now is the clock; default time.Now.
	Now func() time.Time
}

// Runner claims and runs sessions.
type Runner struct {
	o      Options
	eng    *engine.Engine
	wake   chan struct{}
	sem    chan struct{}
	wg     sync.WaitGroup
	active sync.Map // session id -> struct{}
}

// NewRunner returns a Runner.
func NewRunner(o Options) *Runner {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.WorkerID == "" {
		host, _ := os.Hostname()
		o.WorkerID = fmt.Sprintf("%s-%d", host, os.Getpid())
	}
	if o.MaxConcurrent <= 0 {
		o.MaxConcurrent = 2
	}
	if o.PollEvery <= 0 {
		o.PollEvery = 3 * time.Second
	}
	if o.ScheduleEvery <= 0 {
		o.ScheduleEvery = 30 * time.Second
	}
	if o.LeaseSeconds <= 0 {
		o.LeaseSeconds = 60
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	r := &Runner{o: o, wake: make(chan struct{}, 1), sem: make(chan struct{}, o.MaxConcurrent)}
	r.eng = engine.New(o.DB.Queries, engine.Options{
		Logger: o.Logger, Notify: r.publish, LeaseSeconds: o.LeaseSeconds, HeartbeatEvery: o.HeartbeatEvery, Now: o.Now,
	})
	return r
}

// Wake asks the runner to look at the queue now.
func (r *Runner) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// publish sends a progress event locally and to every other process.
func (r *Runner) publish(e progress.Event) {
	if r.o.Hub != nil {
		r.o.Hub.Publish(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := progress.Notify(ctx, r.o.DB.Pool, e); err != nil {
		r.o.Logger.Debug("progress notify failed", "error", err)
	}
}

// Run claims and executes sessions until ctx ends, then waits for running
// sessions to wind down (the engine ends them promptly when ctx is cancelled).
func (r *Runner) Run(ctx context.Context) {
	r.o.Logger.Info("job runner started", "worker", r.o.WorkerID, "max_concurrent", r.o.MaxConcurrent)
	t := time.NewTicker(r.o.PollEvery)
	defer t.Stop()
	for {
		r.claimLoop(ctx)
		select {
		case <-ctx.Done():
			r.wg.Wait()
			r.o.Logger.Info("job runner stopped")
			return
		case <-t.C:
		case <-r.wake:
		}
	}
}

// claimLoop starts sessions while there is capacity and work.
func (r *Runner) claimLoop(ctx context.Context) {
	for ctx.Err() == nil {
		select {
		case r.sem <- struct{}{}:
		default:
			return // at capacity
		}
		sess, err := r.o.DB.ClaimSession(ctx, store.ClaimSessionParams{WorkerID: r.o.WorkerID, LeaseSecs: r.o.LeaseSeconds})
		if err != nil {
			<-r.sem
			if !errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
				r.o.Logger.Error("claim session", "error", err)
			}
			return
		}
		r.wg.Add(1)
		r.active.Store(sess.ID, struct{}{})
		metrics.SyncActive.Inc()
		go func() {
			defer func() {
				r.active.Delete(sess.ID)
				metrics.SyncActive.Dec()
				<-r.sem
				r.wg.Done()
				r.Wake() // capacity freed: look again
			}()
			r.execute(ctx, sess)
		}()
	}
}

// execute runs one claimed session.
func (r *Runner) execute(ctx context.Context, sess store.SyncSession) {
	log := r.o.Logger.With("session", sess.ID, "account", sess.AccountID, "type", sess.SyncType)
	log.Info("session started")
	r.publish(progress.Event{Kind: progress.KindProgress, AccountID: sess.AccountID.String(), SessionID: sess.ID.String()})

	res, err := r.o.Builder.Build(ctx, sess)
	if err != nil {
		r.failBeforeStart(ctx, sess, err)
		return
	}
	if res.Close != nil {
		defer res.Close()
	}
	result := r.eng.Run(ctx, sess, engine.RunDeps{Gmail: res.Gmail, Dest: res.Dest, WorkerID: r.o.WorkerID})
	r.after(ctx, sess, result)
}

// failBeforeStart finishes a session whose clients could not be built (bad
// credentials, destination down).
func (r *Runner) failBeforeStart(ctx context.Context, sess store.SyncSession, cause error) {
	fctx := context.WithoutCancel(ctx)
	status := engine.StatusFailed
	msg := cause.Error()
	switch {
	case ctx.Err() != nil:
		status, msg = engine.StatusAbandoned, "worker shutting down"
	case errors.Is(cause, clients.ErrNotAuthorised), errors.Is(cause, gmail.ErrAuth):
		r.setAccountStatus(fctx, sess.AccountID, "needs_reauth", "Google access is missing or was revoked. Reauthorise the account.")
	case errors.Is(cause, clients.ErrNotConfigured):
		msg = "The Google OAuth client is not configured. Set it up under Settings."
		r.setAccountStatus(fctx, sess.AccountID, "error", msg)
	case imapdest.KindOf(cause) == imapdest.KindAuth:
		r.setAccountStatus(fctx, sess.AccountID, "error", "The IMAP server rejected the destination credentials.")
	}
	r.o.Logger.Warn("session could not start", "session", sess.ID, "error", cause)
	_ = r.o.DB.InsertEvent(fctx, store.InsertEventParams{SessionID: sess.ID, EventType: "failed", Severity: "error",
		Message: "Could not start: " + trimMsg(msg), Metadata: []byte("{}")})
	fin, err := r.o.DB.FinishSession(fctx, store.FinishSessionParams{ID: sess.ID, Status: status, LastError: trimMsg(msg)})
	if err != nil {
		r.o.Logger.Error("finish session", "error", err)
		return
	}
	r.after(ctx, sess, engine.Result{Session: fin, Err: cause, AuthFailed: errors.Is(cause, gmail.ErrAuth) || errors.Is(cause, clients.ErrNotAuthorised)})
}

func (r *Runner) setAccountStatus(ctx context.Context, id uuid.UUID, status, msg string) {
	if err := r.o.DB.SetAccountStatus(ctx, store.SetAccountStatusParams{ID: id, Status: status, LastConnectionError: msg}); err != nil {
		r.o.Logger.Warn("set account status", "error", err)
	}
}

// after records metrics, requeues abandoned work and sends notifications.
func (r *Runner) after(ctx context.Context, sess store.SyncSession, res engine.Result) {
	s := res.Session
	if s.ID == uuid.Nil { // lease lost: another worker owns it
		return
	}
	recordMetrics(s)
	fctx := context.WithoutCancel(ctx)
	if s.Status == engine.StatusAbandoned {
		r.requeue(fctx, s)
	}
	if r.o.Notifier != nil {
		r.o.Notifier.SessionFinished(fctx, s, res.AuthFailed)
	}
	r.o.Logger.Info("session finished", "session", s.ID, "status", s.Status, "copied", s.CopiedCount, "skipped", s.SkippedCount, "failed", s.FailedCount)
}

func recordMetrics(s store.SyncSession) {
	metrics.SessionsTotal.WithLabelValues(s.SyncType, s.Status).Inc()
	metrics.MessagesDiscovered.Add(float64(s.DiscoveredCount))
	metrics.MessagesCopied.Add(float64(s.CopiedCount))
	metrics.MessagesSkipped.Add(float64(s.SkippedCount))
	metrics.MessagesFailed.Add(float64(s.FailedCount))
	metrics.MessagesRecovered.Add(float64(s.RecoveredCount))
	metrics.BytesCopied.Add(float64(s.BytesCopied))
	if s.DurationMs.Valid {
		metrics.SessionDuration.Observe(float64(s.DurationMs.Int64) / 1000)
	}
	if s.Status == engine.StatusCompleted || s.Status == engine.StatusCompletedWithErrors {
		metrics.LastSuccess.WithLabelValues(s.AccountID.String()).SetToCurrentTime()
	}
}

// maxAbandonedStreak stops a session that keeps killing its worker (for
// example by running out of memory) from being requeued forever.
const maxAbandonedStreak = 3

// requeue enqueues a recovery session after an abandoned one.
func (r *Runner) requeue(ctx context.Context, s store.SyncSession) {
	n, err := r.o.DB.CountRecentAbandoned(ctx, store.CountRecentAbandonedParams{
		AccountID: s.AccountID, CreatedAt: pgtype.Timestamptz{Time: r.o.Now().Add(-time.Hour), Valid: true}})
	if err == nil && n > maxAbandonedStreak {
		r.o.Logger.Error("session abandoned repeatedly; not requeueing", "account", s.AccountID, "count", n)
		_ = r.o.DB.InsertEvent(ctx, store.InsertEventParams{SessionID: s.ID, EventType: "requeue_stopped", Severity: "error",
			Message: "This session was interrupted several times in the last hour; automatic recovery stopped. Start a sync manually.", Metadata: []byte("{}")})
		return
	}
	if _, err := r.enqueue(ctx, s.AccountID, "recovery", s.SyncType, s.Params, nil); err != nil {
		r.o.Logger.Error("requeue after abandoned session", "error", err)
	}
}

// enqueue creates a queued session; an existing queued or running session for
// the account (the unique index) makes it a no-op (created=false).
func (r *Runner) enqueue(ctx context.Context, account uuid.UUID, trigger, syncType string, params []byte, runAt *time.Time) (created bool, err error) {
	if len(params) == 0 {
		params = []byte("{}")
	}
	arg := store.CreateSessionParams{ID: uuid.New(), AccountID: account, TriggerType: trigger, SyncType: syncType, Params: params}
	if runAt != nil {
		arg.RunAt = pgtype.Timestamptz{Time: *runAt, Valid: true}
	}
	sess, err := r.o.DB.CreateSession(ctx, arg)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return false, nil
		}
		return false, err
	}
	r.publish(progress.Event{Kind: progress.KindQueued, AccountID: account.String(), SessionID: sess.ID.String()})
	r.Wake()
	return true, nil
}

func trimMsg(s string) string {
	r := []rune(s)
	if len(r) > 500 {
		return string(r[:500]) + "…"
	}
	return s
}
