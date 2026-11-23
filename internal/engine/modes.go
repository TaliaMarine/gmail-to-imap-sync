package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/gmail"
)

// initialCursor is stored in sync_state.initial_sync_cursor so an interrupted
// initial import resumes where it stopped. It is only valid for the query it
// was created with.
type initialCursor struct {
	Query     string `json:"query"`
	PageToken string `json:"page_token"`
}

// initialRun imports the whole mailbox (or everything after the configured
// date). The history ID is taken BEFORE enumerating so changes made while the
// import runs are picked up by the first incremental sync.
func (r *run) initialRun(ctx context.Context) error {
	r.setPhase(PhaseEnumerate)
	profile, err := r.d.Gmail.Profile(ctx)
	if err != nil {
		return fmt.Errorf("read profile: %w", err)
	}
	if err := r.e.st.BeginInitialSync(ctx, store.BeginInitialSyncParams{
		AccountID: r.sess.AccountID, StartHistoryID: i8(profile.HistoryID)}); err != nil {
		return err
	}
	if st, err := r.e.st.GetSyncState(ctx, r.sess.AccountID); err == nil {
		r.state = st // may carry the start_history_id of an earlier, interrupted attempt
	}
	_ = r.e.st.SetSessionStartingHistory(ctx, store.SetSessionStartingHistoryParams{ID: r.sess.ID, StartingHistoryID: r.state.StartHistoryID})

	var after time.Time
	if r.cfg.InitialSyncAfter.Valid {
		after = r.cfg.InitialSyncAfter.Time
	}
	query := gmailQuery(after)

	var cur initialCursor
	if len(r.state.InitialSyncCursor) > 0 {
		_ = json.Unmarshal(r.state.InitialSyncCursor, &cur)
	}
	if cur.PageToken != "" && cur.Query != query {
		// The settings changed since the cursor was written: start over. The
		// destination deduplicates, so nothing is delivered twice.
		r.event(ctx, "restart", "info", "Initial import restarted because the date limit changed", nil)
		cur = initialCursor{}
	}
	if cur.PageToken != "" {
		r.event(ctx, "resume", "info", "Resuming the initial import where it stopped", nil)
	}

	token := cur.PageToken
	for {
		if r.stopping() {
			return errIncomplete
		}
		r.setPhase(PhaseEnumerate)
		page, err := r.d.Gmail.ListMessages(ctx, gmail.ListOptions{Query: query, PageToken: token, PageSize: r.e.opts.PageSize})
		if err != nil {
			return fmt.Errorf("list messages: %w", err)
		}
		ids := idsOf(page.Messages)
		r.c.discovered.Add(int64(len(ids)))

		r.setPhase(PhaseDelivering)
		complete, err := r.processBatch(ctx, ids, false)
		if err != nil {
			return err
		}
		if !complete {
			return errIncomplete
		}
		r.batchDone(ctx)

		r.setPhase(PhaseCheckpoint)
		if page.NextPageToken == "" {
			break
		}
		token = page.NextPageToken
		if err := r.e.st.SaveInitialCursor(ctx, store.SaveInitialCursorParams{
			AccountID: r.sess.AccountID, InitialSyncCursor: mustJSON(initialCursor{Query: query, PageToken: token})}); err != nil {
			return fmt.Errorf("save cursor: %w", err)
		}
	}
	if err := r.e.st.CompleteInitialSync(ctx, r.sess.AccountID); err != nil {
		return fmt.Errorf("complete initial sync: %w", err)
	}
	r.endHist = r.state.StartHistoryID
	r.event(ctx, "initial_completed", "info", "Initial import finished", nil)
	return nil
}

// incrementalRun applies Gmail history since the stored checkpoint.
func (r *run) incrementalRun(ctx context.Context) error {
	start := uint64(r.state.LastSuccessfulHistoryID.Int64)
	_ = r.e.st.SetSessionStartingHistory(ctx, store.SetSessionStartingHistoryParams{ID: r.sess.ID, StartingHistoryID: r.state.LastSuccessfulHistoryID})

	token := ""
	for {
		if r.stopping() {
			return errIncomplete
		}
		r.setPhase(PhaseHistory)
		page, err := r.d.Gmail.History(ctx, start, token)
		if errors.Is(err, gmail.ErrHistoryExpired) {
			r.event(ctx, "history_expired", "warning", "Gmail no longer has the history since the last checkpoint; reconciling recent messages instead", nil)
			r.setType(ctx, "reconcile")
			return r.reconcileRun(ctx, true)
		}
		if err != nil {
			return fmt.Errorf("read history: %w", err)
		}

		ids := r.newIDs(page)
		r.c.discovered.Add(int64(len(ids)))
		r.setPhase(PhaseDelivering)
		complete, err := r.processBatch(ctx, ids, false)
		if err != nil {
			return err
		}
		if !complete {
			return errIncomplete
		}
		r.batchDone(ctx)

		r.setPhase(PhaseCheckpoint)
		if page.NextPageToken != "" {
			// More history follows; the response's historyId describes the
			// mailbox NOW, so only the last record of this page is safe.
			if page.LastRecordID > 0 {
				if err := r.saveCheckpoint(ctx, page.LastRecordID); err != nil {
					return err
				}
			}
			token = page.NextPageToken
			continue
		}
		if err := r.saveFinalCheckpoint(ctx, page.HistoryID, r.startedAt); err != nil {
			return err
		}
		r.endHist = pgtype.Int8{Int64: int64(page.HistoryID), Valid: true}
		return nil
	}
}

// saveFinalCheckpoint stores the checkpoint after the last page together with
// the time the history was read (the run's start: conservative), which is the
// cutoff a later reconcile uses. Intermediate checkpoints must not do this.
func (r *run) saveFinalCheckpoint(ctx context.Context, id uint64, readAt time.Time) error {
	if err := r.e.st.SaveFinalCheckpoint(ctx, store.SaveFinalCheckpointParams{
		AccountID: r.sess.AccountID, HistoryID: i8(id), At: pgtype.Timestamptz{Time: readAt, Valid: true}}); err != nil {
		return fmt.Errorf("save checkpoint: %w", err)
	}
	return nil
}

func (r *run) saveCheckpoint(ctx context.Context, id uint64) error {
	if err := r.e.st.SaveCheckpoint(ctx, store.SaveCheckpointParams{
		AccountID: r.sess.AccountID, LastSuccessfulHistoryID: i8(id)}); err != nil {
		return fmt.Errorf("save checkpoint: %w", err)
	}
	return nil
}

// newIDs returns the message IDs of a history page, each once. A message that
// is also being retried this session is skipped. Messages seen on an earlier
// page are NOT skipped: a label may have been added after the first fetch, and
// fetching again is cheap while the destination deduplicates.
func (r *run) newIDs(p gmail.HistoryPage) []string {
	local := map[string]struct{}{}
	var out []string
	add := func(id string) {
		if id == "" {
			return
		}
		if _, dup := local[id]; dup {
			return
		}
		if _, retried := r.seen[id]; retried {
			return
		}
		local[id] = struct{}{}
		out = append(out, id)
	}
	for _, m := range p.MessagesAdded {
		add(m.ID)
	}
	for _, l := range p.LabelsAdded {
		add(l.MessageID)
	}
	return out
}

// reconcileRun lists recent messages instead of reading history: after an
// expired checkpoint, or when the user rescans a date range or label. The
// destination's deduplication absorbs the overlap with what is already there.
//
// expired=true is the recovery case: the checkpoint is moved to the history
// ID read before listing. A user rescan never moves it.
func (r *run) reconcileRun(ctx context.Context, expired bool) error {
	r.setPhase(PhaseEnumerate)
	listStart := r.e.opts.Now()
	profile, err := r.d.Gmail.Profile(ctx) // before listing, so nothing slips between
	if err != nil {
		return fmt.Errorf("read profile: %w", err)
	}
	var after time.Time
	switch {
	case r.params.After != "":
		after, err = time.Parse("2006-01-02", r.params.After)
		if err != nil {
			return &configError{fmt.Errorf("invalid reconcile date %q", r.params.After)}
		}
	case r.state.HistoryCheckpointAt.Valid:
		// From when the stored history ID was read, not from the last time
		// anything succeeded: a rescan of a label or a date range must not
		// shorten the window that an expired checkpoint needs to cover.
		after = r.state.HistoryCheckpointAt.Time.AddDate(0, 0, -2) // Gmail searches by day
	case r.cfg.InitialSyncAfter.Valid:
		after = r.cfg.InitialSyncAfter.Time
	}
	r.advance = expired && r.params.LabelID == "" && r.params.After == ""
	query := gmailQuery(after)

	var labels []string
	if r.params.LabelID != "" {
		labels = []string{r.params.LabelID}
	}
	token := ""
	for {
		if r.stopping() {
			return errIncomplete
		}
		r.setPhase(PhaseEnumerate)
		page, err := r.d.Gmail.ListMessages(ctx, gmail.ListOptions{Query: query, LabelIDs: labels, PageToken: token, PageSize: r.e.opts.PageSize})
		if err != nil {
			return fmt.Errorf("list messages: %w", err)
		}
		ids := idsOf(page.Messages)
		r.c.discovered.Add(int64(len(ids)))
		r.setPhase(PhaseDelivering)
		complete, err := r.processBatch(ctx, ids, false)
		if err != nil {
			return err
		}
		if !complete {
			return errIncomplete
		}
		r.batchDone(ctx)
		if token = page.NextPageToken; token == "" {
			break
		}
	}
	r.setPhase(PhaseCheckpoint)
	if r.advance {
		if err := r.saveFinalCheckpoint(ctx, profile.HistoryID, listStart); err != nil {
			return err
		}
		r.endHist = pgtype.Int8{Int64: int64(profile.HistoryID), Valid: true}
	} else if err := r.e.st.TouchLastSuccess(ctx, r.sess.AccountID); err != nil {
		return err
	}
	r.event(ctx, "reconcile_completed", "info", "Reconciliation finished", nil)
	return nil
}

// retryRun processes exactly the failures named in the session parameters.
func (r *run) retryRun(ctx context.Context) error {
	ids := make([]uuid.UUID, 0, len(r.params.FailureIDs))
	for _, s := range r.params.FailureIDs {
		id, err := uuid.Parse(s)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	rows, err := r.e.st.FailuresByIDs(ctx, ids)
	if err != nil {
		return fmt.Errorf("load failures: %w", err)
	}
	var mine []store.FailedMessage
	for _, f := range rows {
		if f.AccountID == r.sess.AccountID {
			mine = append(mine, f)
		}
	}
	return r.processFailures(ctx, mine)
}

// retryDue processes failures whose backoff has passed, before the main work.
func (r *run) retryDue(ctx context.Context) error {
	rows, err := r.e.st.DueFailures(ctx, store.DueFailuresParams{AccountID: r.sess.AccountID, Limit: r.e.opts.DueFailureLimit})
	if err != nil {
		return fmt.Errorf("load due failures: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	return r.processFailures(ctx, rows)
}

func (r *run) processFailures(ctx context.Context, rows []store.FailedMessage) error {
	if len(rows) == 0 {
		return nil
	}
	r.setPhase(PhaseFailures)
	ids := make([]string, 0, len(rows))
	r.mu.Lock()
	for _, f := range rows {
		r.retryIDs[f.GmailMessageID] = f.ID
		r.seen[f.GmailMessageID] = struct{}{} // the history must not retry it again this session
		ids = append(ids, f.GmailMessageID)
	}
	r.mu.Unlock()
	r.c.discovered.Add(int64(len(ids)))
	r.event(ctx, "retry", "info", fmt.Sprintf("Retrying %d failed messages", len(ids)), map[string]any{"count": len(ids)})
	complete, err := r.processBatch(ctx, ids, true)
	if err != nil {
		return err
	}
	if !complete {
		return errIncomplete
	}
	return nil
}

func idsOf(refs []gmail.MessageRef) []string {
	out := make([]string, len(refs))
	for i, m := range refs {
		out[i] = m.ID
	}
	return out
}

func i8(v uint64) pgtype.Int8 { return pgtype.Int8{Int64: int64(v), Valid: true} }
