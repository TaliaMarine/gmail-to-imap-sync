// Package engine runs one synchronisation session: it reads messages from
// Gmail, decides their destination folders, delivers them to the IMAP server,
// records failures and moves the checkpoints.
//
// Ordering rules that keep it recoverable (FULL_SPECS.md §13):
//   - a checkpoint only moves past a batch whose every message was either
//     delivered, found already present, or persisted as a failure;
//   - on any doubt (cancellation, lost lease, a database error while recording
//     a failure) the batch counts as unhandled and the checkpoint stays put;
//   - re-processing is always safe because the destination deduplicates.
package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/failures"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/gmail"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/imapdest"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/mapping"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/progress"
)

// Phases shown in the UI.
const (
	PhaseLabels     = "loading_labels"
	PhaseHistory    = "reading_history"
	PhaseEnumerate  = "enumerating"
	PhaseDelivering = "copying"
	PhaseFailures   = "processing_failures"
	PhaseCheckpoint = "saving_checkpoint"
)

// Session statuses written by the engine.
const (
	StatusCompleted           = "completed"
	StatusCompletedWithErrors = "completed_with_errors"
	StatusFailed              = "failed"
	StatusCancelled           = "cancelled"
	StatusAbandoned           = "abandoned"
)

var (
	errCancelled = errors.New("session cancelled")
	errLeaseLost = errors.New("session lease lost")
	// errIncomplete: a batch was left unfinished (stop requested); not a failure.
	errIncomplete = errors.New("batch incomplete")
)

// Options tune an Engine.
type Options struct {
	Logger *slog.Logger
	// Notify publishes progress events (to the in-process hub and
	// PostgreSQL). May be nil.
	Notify func(progress.Event)
	// Now is the clock; default time.Now.
	Now func() time.Time
	// HeartbeatEvery is how often progress and the lease are written;
	// default 1s.
	HeartbeatEvery time.Duration
	// LeaseSeconds is the lease extension per heartbeat; default 60.
	LeaseSeconds int32
	// PageSize is the Gmail list page size; default 500.
	PageSize int64
	// DueFailureLimit caps automatic failure retries per session; default 500.
	DueFailureLimit int32
}

// Engine executes sessions.
type Engine struct {
	st   Store
	opts Options
}

// New returns an Engine.
func New(st Store, o Options) *Engine {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.HeartbeatEvery <= 0 {
		o.HeartbeatEvery = time.Second
	}
	if o.LeaseSeconds <= 0 {
		o.LeaseSeconds = 60
	}
	if o.PageSize <= 0 {
		o.PageSize = 500
	}
	if o.DueFailureLimit <= 0 {
		o.DueFailureLimit = 500
	}
	return &Engine{st: st, opts: o}
}

// RunDeps are the per-session collaborators.
type RunDeps struct {
	Gmail    gmail.Source
	Dest     Dest
	WorkerID string
}

// Result is what a finished session reports back to the runner.
type Result struct {
	Session store.SyncSession
	// Err is the error that ended the session, nil for completed sessions
	// (also those with failed messages) and for cancellation.
	Err error
	// AuthFailed: Gmail credentials are no longer valid.
	AuthFailed bool
}

// counters are the live statistics of a session.
type counters struct {
	discovered, processed, copied, skipped, failed, recovered atomic.Int64
}

// run is the state of one executing session.
type run struct {
	e    *Engine
	log  *slog.Logger
	sess store.SyncSession
	d    RunDeps

	acct   store.Account
	cfg    store.AccountSyncSetting
	rules  mapping.Settings
	state  store.SyncState
	params Params

	workCtx context.Context
	cancel  context.CancelCauseFunc

	graceful atomic.Bool // stop dispatching, finish what is in flight
	c        counters

	mu           sync.Mutex
	phase        string
	folder       string
	catalog      map[string]mapping.Label
	seen         map[string]struct{}
	triedUnknown map[string]bool
	retryIDs     map[string]uuid.UUID // gmail id -> failure row, for messages being retried
	pageNote     pageNote
	endHist      pgtype.Int8
	advance      bool // reconcile may move the checkpoint
	startedAt    time.Time
	lastBeat     atomic.Int64 // unix nanos of the last successful heartbeat
}

type pageNote struct {
	at                                  time.Time
	processed, copied, skipped, failedN int64
}

// Run executes the session sess, which must be leased to d.WorkerID. It
// always finishes the session row (unless the lease was lost, in which case
// another worker owns it).
func (e *Engine) Run(ctx context.Context, sess store.SyncSession, d RunDeps) Result {
	r := &run{e: e, log: e.opts.Logger.With("session", sess.ID, "account", sess.AccountID), sess: sess, d: d,
		catalog: map[string]mapping.Label{}, seen: map[string]struct{}{}, triedUnknown: map[string]bool{}, retryIDs: map[string]uuid.UUID{}}
	r.startedAt = e.opts.Now()
	r.lastBeat.Store(time.Now().UnixNano())
	workCtx, cancel := context.WithCancelCause(ctx)
	r.workCtx, r.cancel = workCtx, cancel
	defer cancel(nil)

	hbDone := make(chan struct{})
	hbCtx, stopHB := context.WithCancel(context.Background())
	go func() { defer close(hbDone); r.heartbeatLoop(hbCtx) }()

	err := r.execute(workCtx)

	stopHB()
	<-hbDone
	return r.finish(ctx, err)
}

func (r *run) setPhase(p string) {
	r.mu.Lock()
	r.phase = p
	r.mu.Unlock()
}

func (r *run) setFolder(f string) {
	r.mu.Lock()
	r.folder = f
	r.mu.Unlock()
}

func (r *run) stopping() bool { return r.graceful.Load() || r.workCtx.Err() != nil }

// execute loads configuration, then runs the right kind of sync.
func (r *run) execute(ctx context.Context) error {
	var err error
	if r.params, err = parseParams(r.sess.Params); err != nil {
		return fmt.Errorf("session params: %w", err)
	}
	if r.acct, err = r.e.st.GetAccount(ctx, r.sess.AccountID); err != nil {
		return fmt.Errorf("load account: %w", err)
	}
	if r.cfg, err = r.e.st.GetSyncSettings(ctx, r.sess.AccountID); err != nil {
		return fmt.Errorf("load sync settings: %w", err)
	}
	if r.rules, err = MappingSettings(r.cfg, r.d.Dest.Delimiter()); err != nil {
		return &configError{err}
	}
	if err = r.e.st.EnsureSyncState(ctx, r.sess.AccountID); err != nil {
		return err
	}
	if r.state, err = r.e.st.GetSyncState(ctx, r.sess.AccountID); err != nil {
		return err
	}
	if keys, err := r.e.st.PendingFailureKeys(ctx, r.sess.AccountID); err == nil {
		// Any pending failure whose message gets delivered by whatever route
		// is resolved, not only the ones retried explicitly.
		for _, k := range keys {
			r.retryIDs[k.GmailMessageID] = k.ID
		}
	} else {
		return fmt.Errorf("load pending failures: %w", err)
	}
	r.event(ctx, "started", "info", fmt.Sprintf("Started %s synchronisation", r.sess.SyncType), nil)

	r.setPhase(PhaseLabels)
	if err := r.refreshLabels(ctx, true); err != nil {
		return err
	}

	switch r.sess.SyncType {
	case "retry":
		return r.retryRun(ctx)
	case "reconcile":
		if err := r.retryDue(ctx); err != nil {
			return err
		}
		return r.reconcileRun(ctx, false)
	}

	// initial and incremental: retry what is due first, then the main work.
	if err := r.retryDue(ctx); err != nil {
		return err
	}
	if !r.state.InitialSyncCompleted || !r.state.LastSuccessfulHistoryID.Valid {
		if r.sess.SyncType != "initial" {
			r.setType(ctx, "initial")
		}
		return r.initialRun(ctx)
	}
	return r.incrementalRun(ctx)
}

type configError struct{ error }

func (r *run) setType(ctx context.Context, t string) {
	if err := r.e.st.SetSessionType(ctx, store.SetSessionTypeParams{ID: r.sess.ID, SyncType: t}); err != nil {
		r.log.Warn("could not update session type", "type", t, "error", err)
	}
	r.sess.SyncType = t
}

// refreshLabels reloads the Gmail label catalog and stores it.
func (r *run) refreshLabels(ctx context.Context, _ bool) error {
	labels, err := r.d.Gmail.Labels(ctx)
	if err != nil {
		return fmt.Errorf("load labels: %w", err)
	}
	cat := make(map[string]mapping.Label, len(labels))
	for _, l := range labels {
		cat[l.ID] = mapping.Label{ID: l.ID, Name: l.Name, System: l.System}
		typ := "user"
		if l.System {
			typ = "system"
		}
		if err := r.e.st.UpsertLabel(ctx, store.UpsertLabelParams{AccountID: r.sess.AccountID, LabelID: l.ID, Name: l.Name, Type: typ}); err != nil {
			return fmt.Errorf("store label: %w", err)
		}
	}
	r.mu.Lock()
	r.catalog = cat
	r.mu.Unlock()
	return nil
}

func (r *run) catalogSnapshot() map[string]mapping.Label {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.catalog
}

// --- batches -------------------------------------------------------------

// processBatch handles message IDs with a bounded worker pool. It reports
// whether every message was handled (delivered, present, or recorded as a
// failure). A fatal error (database trouble) aborts the batch.
func (r *run) processBatch(ctx context.Context, ids []string, retry bool) (complete bool, err error) {
	workers := int(r.cfg.WorkerCount)
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan string)
	var (
		wg        sync.WaitGroup
		unhandled atomic.Int64
		fatalMu   sync.Mutex
		fatal     error
	)
	setFatal := func(err error) {
		fatalMu.Lock()
		if fatal == nil {
			fatal = err
			r.cancel(err) // stop the other workers: the checkpoint must not move
		}
		fatalMu.Unlock()
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				handled, err := r.processMessage(ctx, id, retry)
				if err != nil {
					setFatal(err)
					unhandled.Add(1)
					continue
				}
				if !handled {
					unhandled.Add(1)
				}
			}
		}()
	}
	dispatched := 0
dispatch:
	for _, id := range ids {
		if r.stopping() {
			break
		}
		select {
		case jobs <- id:
			dispatched++
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()

	fatalMu.Lock()
	defer fatalMu.Unlock()
	if fatal != nil {
		return false, fatal
	}
	return dispatched == len(ids) && unhandled.Load() == 0, nil
}

// processMessage handles one message. handled=false means the outcome is not
// recorded anywhere (context ended), so the batch is incomplete. A non-nil
// error is fatal for the session.
func (r *run) processMessage(ctx context.Context, id string, retry bool) (handled bool, err error) {
	defer func() {
		if handled && err == nil {
			r.c.processed.Add(1)
		}
	}()
	msg, gerr := r.d.Gmail.GetRaw(ctx, id)
	if gerr != nil {
		if ctx.Err() != nil {
			return false, nil
		}
		return r.failure(ctx, id, nil, nil, nil, nil, gerr)
	}

	cat := r.catalogSnapshot()
	plan := mapping.Decide(msg.LabelIDs, cat, r.rules)
	if r.newUnknown(plan.Unknown) {
		// A label we have never seen: ask Gmail again, then re-plan. Each
		// distinct unknown ID triggers at most one refresh per session.
		if rerr := r.refreshLabels(ctx, true); rerr == nil {
			plan = mapping.Decide(msg.LabelIDs, r.catalogSnapshot(), r.rules)
		} else if ctx.Err() != nil {
			return false, nil
		}
	}
	if plan.Skip {
		r.c.skipped.Add(1)
		return true, r.resolveIfRetrying(ctx, id)
	}

	results := r.d.Dest.Deliver(ctx, imapdest.Message{
		ID:    imapdest.HeaderValue(r.sess.AccountID.String(), id),
		Raw:   msg.Raw,
		Date:  msg.InternalDate,
		Flags: plan.Flags,
	}, plan.Folders)

	var done, failedF []string
	var firstErr error
	var delivered int
	for _, res := range results {
		switch res.Status {
		case imapdest.Delivered:
			delivered++
			done = append(done, res.Folder)
			r.setFolder(res.Folder)
		case imapdest.Skipped:
			done = append(done, res.Folder)
		default:
			failedF = append(failedF, res.Folder)
			if firstErr == nil {
				firstErr = res.Err
			}
		}
	}
	if firstErr != nil {
		if ctx.Err() != nil && isCtxError(firstErr) {
			return false, nil // interrupted, not failed
		}
		return r.failure(ctx, id, msg, plan.Folders, done, failedF, firstErr)
	}
	if delivered > 0 {
		r.c.copied.Add(1)
	} else {
		r.c.skipped.Add(1)
	}
	return true, r.resolveIfRetrying(ctx, id)
}

// newUnknown reports whether ids holds a label ID not yet tried this session,
// and remembers them all.
func (r *run) newUnknown(ids []string) bool {
	if len(ids) == 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	fresh := false
	for _, id := range ids {
		if !r.triedUnknown[id] {
			r.triedUnknown[id] = true
			fresh = true
		}
	}
	return fresh
}

func isCtxError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// resolveIfRetrying deletes the failure record of a message that has now been
// handled successfully. Only messages being retried can have one in normal
// operation, so no per-message lookup is done otherwise.
func (r *run) resolveIfRetrying(ctx context.Context, gmailID string) error {
	r.mu.Lock()
	rowID, ok := r.retryIDs[gmailID]
	if ok {
		delete(r.retryIDs, gmailID)
	}
	r.mu.Unlock()
	if !ok {
		return nil
	}
	if err := r.e.st.ResolveFailure(ctx, rowID); err != nil {
		return fmt.Errorf("resolve failure: %w", err)
	}
	r.c.recovered.Add(1)
	return nil
}

// failure records a failed message. It returns handled=true once the failure
// is persisted; if persisting fails the error is fatal.
func (r *run) failure(ctx context.Context, gmailID string, msg *gmail.Message, intended, done, failed []string, cause error) (bool, error) {
	cat, retryable := failures.Categorise(cause)
	now := r.e.opts.Now()

	prev, perr := r.e.st.GetFailureByMessage(ctx, store.GetFailureByMessageParams{AccountID: r.sess.AccountID, GmailMessageID: gmailID})
	havePrev := perr == nil
	if perr != nil && !errors.Is(perr, pgx.ErrNoRows) && !errors.Is(perr, sql.ErrNoRows) {
		return false, fmt.Errorf("look up failure: %w", perr)
	}
	attempts := 1
	if havePrev {
		attempts = int(prev.AttemptCount) + 1
	}
	next, exhausted := failures.Schedule(now, attempts, int(r.cfg.RetryMaxAttempts), retryable)

	p := store.UpsertFailureParams{
		ID: uuid.New(), AccountID: r.sess.AccountID, GmailMessageID: gmailID,
		ErrorCategory: cat, ErrorMessage: failures.Truncate(cause.Error()),
		RetryExhausted: exhausted, LastSessionID: uuid.NullUUID{UUID: r.sess.ID, Valid: true},
		ErrorHistory: failures.MergeHistory(prevHistory(prev, havePrev), failures.HistoryEntry{
			At: now, Category: cat, Message: failures.Truncate(cause.Error()), Session: r.sess.ID.String()}),
	}
	if !next.IsZero() {
		p.NextRetryAt = pgtype.Timestamptz{Time: next, Valid: true}
	}
	switch {
	case msg != nil:
		meta := failures.ExtractMeta(msg.Raw)
		p.GmailThreadID, p.Subject, p.Sender = msg.ThreadID, meta.Subject, meta.Sender
		p.Recipients = mustJSON(meta.Recipients)
		if !meta.Date.IsZero() {
			p.MessageDate = pgtype.Timestamptz{Time: meta.Date, Valid: true}
		} else if !msg.InternalDate.IsZero() {
			p.MessageDate = pgtype.Timestamptz{Time: msg.InternalDate, Valid: true}
		}
	case havePrev: // keep what we knew
		p.GmailThreadID, p.Subject, p.Sender, p.Recipients, p.MessageDate = prev.GmailThreadID, prev.Subject, prev.Sender, prev.Recipients, prev.MessageDate
	default:
		p.Recipients = mustJSON([]string{})
	}
	p.IntendedFolders, p.CompletedFolders, p.FailedFolders = mustJSON(nz(intended)), mustJSON(nz(done)), mustJSON(nz(failed))

	row, err := r.e.st.UpsertFailure(ctx, p)
	if err != nil {
		return false, fmt.Errorf("record failure for %s: %w", gmailID, err)
	}
	if errors.Is(cause, gmail.ErrMessageNotFound) || errors.Is(cause, gmail.ErrMessageTooLarge) {
		// Gone from Gmail (or unfetchable): nothing to retry, keep it visible.
		if _, err := r.e.st.SetFailureStatus(ctx, store.SetFailureStatusParams{ID: row.ID, Status: "ignored"}); err != nil {
			return false, fmt.Errorf("ignore failure: %w", err)
		}
		r.c.skipped.Add(1)
		r.dropRetry(gmailID)
		return true, nil
	}
	r.c.failed.Add(1)
	r.dropRetry(gmailID)
	if errors.Is(cause, gmail.ErrAuth) {
		return false, cause // credentials are gone: end the session, do not record dozens more
	}
	return true, nil
}

func (r *run) dropRetry(gmailID string) {
	r.mu.Lock()
	delete(r.retryIDs, gmailID)
	r.mu.Unlock()
}

func prevHistory(f store.FailedMessage, ok bool) []byte {
	if !ok {
		return nil
	}
	return f.ErrorHistory
}

func nz(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("null")
	}
	return b
}

// --- progress, events, finishing ----------------------------------------

func (r *run) heartbeatLoop(ctx context.Context) {
	t := time.NewTicker(r.e.opts.HeartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.beat(ctx)
		}
	}
}

func (r *run) beat(ctx context.Context) {
	bctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cancelReq, err := r.e.st.HeartbeatSession(bctx, r.heartbeatParams())
	switch {
	case err == nil:
		r.lastBeat.Store(time.Now().UnixNano())
		if cancelReq.Valid {
			r.graceful.Store(true)
		}
		r.notify(progress.KindProgress)
	case errors.Is(err, pgx.ErrNoRows), errors.Is(err, sql.ErrNoRows):
		r.log.Error("lease lost, stopping")
		r.cancel(errLeaseLost)
	default:
		// A passing database hiccup must not kill the session; the lease has
		// 60 s of slack. Persistent failure ends it through the lease expiry.
		r.log.Warn("heartbeat failed", "error", err)
		// But never run on borrowed time: once the lease may have lapsed, a
		// sweeper can hand the session to another worker, so stop delivering.
		limit := time.Duration(r.e.opts.LeaseSeconds) * time.Second * 2 / 3
		if since := time.Since(time.Unix(0, r.lastBeat.Load())); since > limit {
			r.log.Error("no successful heartbeat within the lease, stopping", "since", since)
			r.cancel(errLeaseLost)
		}
	}
}

func (r *run) heartbeatParams() store.HeartbeatSessionParams {
	r.mu.Lock()
	phase, folder := r.phase, r.folder
	r.mu.Unlock()
	ds := r.d.Dest.Stats()
	return store.HeartbeatSessionParams{
		ID: r.sess.ID, WorkerID: r.d.WorkerID, LeaseSecs: r.e.opts.LeaseSeconds,
		Phase: phase, CurrentFolder: folder,
		Discovered: r.c.discovered.Load(), Processed: r.c.processed.Load(), Copied: r.c.copied.Load(),
		Skipped: r.c.skipped.Load(), Failed: r.c.failed.Load(), Recovered: r.c.recovered.Load(),
		FoldersCreated: ds.FoldersCreated, ApiRequests: r.d.Gmail.Requests(), BytesCopied: ds.BytesDelivered,
	}
}

func (r *run) notify(kind string) {
	if r.e.opts.Notify != nil {
		r.e.opts.Notify(progress.Event{Kind: kind, AccountID: r.sess.AccountID.String(), SessionID: r.sess.ID.String()})
	}
}

// event stores an aggregated session event. Failures to store are logged:
// events are diagnostics, they must never stop a synchronisation.
func (r *run) event(ctx context.Context, typ, severity, msg string, meta map[string]any) {
	var b []byte
	if meta != nil {
		b = mustJSON(meta)
	} else {
		b = []byte("{}")
	}
	if err := r.e.st.InsertEvent(context.WithoutCancel(ctx), store.InsertEventParams{
		SessionID: r.sess.ID, EventType: typ, Severity: severity, Message: msg, Metadata: b}); err != nil {
		r.log.Warn("could not store session event", "error", err)
	}
}

// batchDone is called after each fully handled batch. It writes at most one
// aggregated "progress" event every few seconds.
func (r *run) batchDone(ctx context.Context) {
	r.mu.Lock()
	n := &r.pageNote
	now := r.e.opts.Now()
	proc, cop, skp, fail := r.c.processed.Load(), r.c.copied.Load(), r.c.skipped.Load(), r.c.failed.Load()
	if !n.at.IsZero() && now.Sub(n.at) < 5*time.Second {
		r.mu.Unlock()
		return
	}
	dp, dc, ds, df := proc-n.processed, cop-n.copied, skp-n.skipped, fail-n.failedN
	*n = pageNote{at: now, processed: proc, copied: cop, skipped: skp, failedN: fail}
	r.mu.Unlock()
	if dp == 0 {
		return
	}
	sev := "info"
	if df > 0 {
		sev = "warning"
	}
	r.event(ctx, "progress", sev, fmt.Sprintf("Handled %d messages: %d copied, %d already present or skipped, %d failed", dp, dc, ds, df),
		map[string]any{"processed": dp, "copied": dc, "skipped": ds, "failed": df})
}

// finish writes the final state of the session.
func (r *run) finish(ctx context.Context, execErr error) Result {
	res := Result{}
	status := StatusCompleted
	lastErr := ""

	cause := context.Cause(r.workCtx)
	switch {
	case errors.Is(cause, errLeaseLost):
		// Another worker owns the session now; leave the row alone.
		res.Err = errLeaseLost
		return res
	case execErr == nil || errors.Is(execErr, errIncomplete):
		switch {
		case r.graceful.Load():
			status = StatusCancelled
		case ctx.Err() != nil:
			status, lastErr = StatusAbandoned, "worker shutting down"
		case r.c.failed.Load() > 0:
			status = StatusCompletedWithErrors
		}
	default:
		switch {
		case ctx.Err() != nil && isCtxError(execErr):
			status, lastErr = StatusAbandoned, "worker shutting down"
		case r.graceful.Load() && isCtxError(execErr):
			status = StatusCancelled
		default:
			status, lastErr = StatusFailed, failures.Truncate(execErr.Error())
			res.Err = execErr
			if errors.Is(execErr, gmail.ErrAuth) {
				res.AuthFailed = true
			}
		}
	}

	fctx := context.WithoutCancel(ctx)
	switch {
	case res.AuthFailed:
		_ = r.e.st.SetAccountStatus(fctx, store.SetAccountStatusParams{ID: r.sess.AccountID, Status: "needs_reauth",
			LastConnectionError: ReauthMessage(r.acct.AuthorisedAt, r.e.opts.Now())})
		r.event(fctx, "auth_failed", "error", "Google rejected the stored credentials: reauthorise this account", nil)
	case status == StatusFailed:
		r.event(fctx, "failed", "error", "Session failed: "+lastErr, nil)
	case status == StatusCancelled:
		r.event(fctx, "cancelled", "info", "Session cancelled; completed work and checkpoint are saved", nil)
	}
	if status == StatusCompleted || status == StatusCompletedWithErrors {
		r.event(fctx, "completed", "info", fmt.Sprintf("Session completed: %d copied, %d skipped, %d failed, %d recovered",
			r.c.copied.Load(), r.c.skipped.Load(), r.c.failed.Load(), r.c.recovered.Load()), nil)
	}

	r.reportRenames(fctx)
	r.reportPacing(fctx)
	ds := r.d.Dest.Stats()
	fctx, cancelFin := context.WithTimeout(fctx, 30*time.Second)
	defer cancelFin()
	fin, err := r.e.st.FinishSession(fctx, store.FinishSessionParams{
		ID: r.sess.ID, Status: status, EndingHistoryID: r.endHist, LastError: lastErr,
		Discovered: r.c.discovered.Load(), Processed: r.c.processed.Load(), Copied: r.c.copied.Load(),
		Skipped: r.c.skipped.Load(), Failed: r.c.failed.Load(), Recovered: r.c.recovered.Load(),
		FoldersCreated: ds.FoldersCreated, ApiRequests: r.d.Gmail.Requests(), BytesCopied: ds.BytesDelivered,
	})
	if err != nil {
		r.log.Error("could not finish session", "error", err)
		if res.Err == nil {
			res.Err = fmt.Errorf("finish session: %w", err)
		}
	}
	res.Session = fin
	r.notify(progress.KindFinished)
	return res
}

// ReauthMessage explains why Google access ended. How long the refresh token
// lived is the best clue: a token that dies after about seven days is the
// signature of an OAuth consent screen still in "Testing", which is a setup
// mistake the owner can fix once and for all.
func ReauthMessage(authorisedAt pgtype.Timestamptz, now time.Time) string {
	const generic = "Google access was revoked or expired. Reauthorise the account."
	if !authorisedAt.Valid {
		return generic
	}
	age := now.Sub(authorisedAt.Time)
	days := int(age.Hours() / 24)
	switch {
	case age >= 6*24*time.Hour && age <= 8*24*time.Hour:
		return fmt.Sprintf("Google access ended %d days after it was granted. That is how long Google keeps access for an app whose consent screen is still in \"Testing\": publish the app (\"In production\") in the Google Cloud console, then reauthorise. See docs/google-setup.md.", days)
	case age < 24*time.Hour:
		return "Google access ended within a day of being granted. Check that the consent screen is published (\"In production\") and that the account was not removed from the app's access in your Google account settings, then reauthorise."
	}
	return fmt.Sprintf("Google access ended %d days after it was granted. Changing the Google password, removing the app under Google account security, or an administrator's policy ends it. Reauthorise the account.", days)
}

// reportRenames records, once per session, the folders the server refused
// under their Gmail-derived name and that were created under a sanitised one.
func (r *run) reportRenames(ctx context.Context) {
	rd, ok := r.d.Dest.(interface{ Renames() map[string]string })
	if !ok {
		return
	}
	m := rd.Renames()
	if len(m) == 0 {
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for i, k := range keys {
		if i == 10 {
			parts = append(parts, fmt.Sprintf("and %d more", len(keys)-10))
			break
		}
		parts = append(parts, fmt.Sprintf("%q as %q", k, m[k]))
	}
	r.event(ctx, "folders_renamed", "warning",
		"The IMAP server does not accept some folder names (Maildir++ forbids \".\"), so these were created under sanitised names: "+strings.Join(parts, ", "),
		map[string]any{"renames": m})
}

// reportPacing tells the owner when Gmail pushed back, and remembers what the
// account tolerated so the next session starts near it instead of rediscovering
// the limit.
func (r *run) reportPacing(ctx context.Context) {
	pg, ok := r.d.Gmail.(interface {
		Pacing() (hits int, paused time.Duration, rate, ceiling float64)
	})
	if !ok {
		return
	}
	hits, paused, rate, ceiling := pg.Pacing()
	if hits > 0 {
		r.event(ctx, "rate_limited", "warning", fmt.Sprintf(
			"Gmail asked the sync to slow down %d times (%s of waiting in total). It now runs at about %.0f messages per second and keeps adjusting.",
			hits, paused.Round(time.Second), rate*250/5),
			map[string]any{"hits": hits, "paused_seconds": int(paused.Seconds()), "rate_share": rate, "ceiling_share": ceiling})
	}
	// Remember the learned ceiling only when something was learned; a quiet
	// session leaves the stored value alone (the next one probes upwards).
	if hits > 0 {
		if err := r.e.st.SetLearnedRate(ctx, store.SetLearnedRateParams{AccountID: r.sess.AccountID,
			LearnedRateFraction: pgtype.Float4{Float32: float32(ceiling), Valid: true}}); err != nil {
			r.log.Warn("could not store the learned rate", "error", err)
		}
	}
}
