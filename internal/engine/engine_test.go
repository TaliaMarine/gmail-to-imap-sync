package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/gmail"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/imapdest"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/progress"
)

type harness struct {
	t  *testing.T
	st *fakeStore
	g  *fakeGmail
	d  *fakeDest
}

func newHarness(t *testing.T) *harness {
	return &harness{t: t, st: newFakeStore(), g: newFakeGmail(), d: newFakeDest()}
}

func (h *harness) engine() *Engine {
	return New(h.st, Options{HeartbeatEvery: 2 * time.Millisecond, PageSize: 500})
}

func (h *harness) session(syncType string, params any) store.SyncSession {
	b, _ := json.Marshal(params)
	if params == nil {
		b = []byte("{}")
	}
	return store.SyncSession{ID: uuid.New(), AccountID: h.st.acct.ID, SyncType: syncType, TriggerType: "manual", Params: b, Status: "running"}
}

func (h *harness) run(ctx context.Context, syncType string, params any) Result {
	return h.engine().Run(ctx, h.session(syncType, params), RunDeps{Gmail: h.g, Dest: h.d, WorkerID: "w1"})
}

func (h *harness) mustRun(syncType string, params any) Result {
	h.t.Helper()
	r := h.run(context.Background(), syncType, params)
	if r.Err != nil {
		h.t.Fatalf("run %s: %v", syncType, r.Err)
	}
	return r
}

func (h *harness) has(folder, id string) bool { return h.d.has(h.st.acct.ID, folder, id) }

func TestInitialImportDeliversEverythingAndSetsCheckpoint(t *testing.T) {
	h := newHarness(t)
	h.g.add("m1", "INBOX", "UNREAD")
	h.g.add("m2", "L1", "L2")               // two user labels
	h.g.add("m3")                           // archived, unlabelled
	h.g.add("m4", "SENT")                   // sent
	h.g.add("m5", "L3")                     // only an excluded label? (not excluded by default: a normal label)
	h.g.add("m6", "SPAM")                   // excluded by default
	h.g.add("m7", "INBOX", "STARRED", "L1") // flag + two folders
	start := h.g.currentHistory()

	r := h.mustRun("initial", nil)

	for folder, ids := range map[string][]string{
		"INBOX": {"m1", "m7"}, "Projects": {"m2", "m7"}, "Finance": {"m2"},
		"Archive": {"m3"}, "Sent": {"m4"}, "Newsletters": {"m5"},
	} {
		for _, id := range ids {
			if !h.has(folder, id) {
				t.Errorf("%s lacks %s", folder, id)
			}
		}
		if h.d.count(folder) != len(ids) {
			t.Errorf("%s holds %d, want %d", folder, h.d.count(folder), len(ids))
		}
	}
	if h.has("Spam", "m6") || len(h.d.folders["Spam"]) != 0 {
		t.Error("spam must not be copied")
	}
	// Flags and date follow the rules.
	m7 := h.d.folders["INBOX"][imapdest.HeaderValue(h.st.acct.ID.String(), "m7")]
	if !reflect.DeepEqual(m7.Flags, []string{`\Seen`, `\Flagged`}) || !m7.Date.Equal(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("m7 = %+v", m7)
	}
	m1 := h.d.folders["INBOX"][imapdest.HeaderValue(h.st.acct.ID.String(), "m1")]
	if len(m1.Flags) != 0 {
		t.Errorf("unread message must not be \\Seen: %v", m1.Flags)
	}

	if r.Session.Status != StatusCompleted || r.Session.CopiedCount != 6 || r.Session.SkippedCount != 1 || r.Session.FailedCount != 0 {
		t.Errorf("session = %+v", r.Session)
	}
	st := h.st.state
	if !st.InitialSyncCompleted || !st.LastSuccessfulHistoryID.Valid || uint64(st.LastSuccessfulHistoryID.Int64) != start || len(st.InitialSyncCursor) != 0 {
		t.Errorf("state = %+v (start history %d)", st, start)
	}
	if len(h.st.labels) != len(h.g.labels) {
		t.Errorf("labels stored = %d", len(h.st.labels))
	}
	if fin := h.st.lastFinish(); fin.FoldersCreated != int64(5) || fin.ApiRequests == 0 {
		t.Errorf("finish = %+v", fin)
	}
}

func TestInitialImportResumesAfterCrashWithoutRedoingPagesOrDuplicating(t *testing.T) {
	h := newHarness(t)
	for i := 1; i <= 7; i++ {
		h.g.add(fmt.Sprintf("m%d", i), "INBOX")
	}
	h.g.listPage = 3
	h.st.cfg.WorkerCount = 1 // deterministic order

	// "Crash" (hard stop) after the 4th delivery: page 1 (m1..m3) is done, page 2 is not.
	ctx, crash := context.WithCancel(context.Background())
	h.d.afterEach = func(n int64) {
		if n == 4 {
			crash()
		}
	}
	r := h.run(ctx, "initial", nil)
	if r.Session.Status != StatusAbandoned {
		t.Fatalf("status after crash = %q (err %v)", r.Session.Status, r.Err)
	}
	if h.st.state.InitialSyncCompleted {
		t.Fatal("initial sync must not be marked complete")
	}
	var cur initialCursor
	if err := json.Unmarshal(h.st.state.InitialSyncCursor, &cur); err != nil || cur.PageToken != "3" {
		t.Fatalf("cursor = %+v (%v), want the start of page 2", cur, err)
	}

	// Restart: a fresh engine, same database, same destination.
	h.d.afterEach = nil
	r = h.mustRun("initial", nil)
	if r.Session.Status != StatusCompleted || !h.st.state.InitialSyncCompleted {
		t.Fatalf("resume = %+v", r.Session)
	}
	for i := 1; i <= 7; i++ {
		id := fmt.Sprintf("m%d", i)
		if h.d.deliveries[imapdest.HeaderValue(h.st.acct.ID.String(), id)+"@INBOX"] != 1 {
			t.Errorf("%s delivered %d times", id, h.d.deliveries[imapdest.HeaderValue(h.st.acct.ID.String(), id)+"@INBOX"])
		}
	}
	for _, id := range []string{"m1", "m2", "m3"} {
		if h.g.getCalls[id] != 1 {
			t.Errorf("%s fetched %d times: page 1 must not be redone after resuming", id, h.g.getCalls[id])
		}
	}
	if h.d.count("INBOX") != 7 {
		t.Errorf("INBOX holds %d", h.d.count("INBOX"))
	}
}

func TestInitialCursorIsDiscardedWhenTheQueryChanges(t *testing.T) {
	h := newHarness(t)
	for i := 1; i <= 4; i++ {
		h.g.add(fmt.Sprintf("m%d", i), "INBOX")
	}
	h.st.state.InitialSyncCursor = mustJSON(initialCursor{Query: "after:2020/01/01", PageToken: "2"})
	h.st.state.StartHistoryID = pgtype.Int8{Int64: 1, Valid: true}
	h.mustRun("initial", nil)
	if h.d.count("INBOX") != 4 {
		t.Errorf("INBOX holds %d: a stale cursor must not skip messages", h.d.count("INBOX"))
	}
}

func TestIncrementalDeliversNewMessagesAndNewLabelCopies(t *testing.T) {
	h := newHarness(t)
	h.g.add("m1", "INBOX")
	h.mustRun("initial", nil)
	before := h.st.state.LastSuccessfulHistoryID.Int64

	h.g.add("m2", "INBOX", "L1")
	h.g.addLabel("m1", "L2") // m1 gains Finance: a second copy, no duplicate in INBOX
	h.g.add("m3", "SPAM")

	r := h.mustRun("incremental", nil)
	if !h.has("INBOX", "m2") || !h.has("Projects", "m2") || !h.has("Finance", "m1") {
		t.Errorf("folders = %v", h.d.folderNames())
	}
	if h.d.deliveries[imapdest.HeaderValue(h.st.acct.ID.String(), "m1")+"@INBOX"] != 1 {
		t.Error("m1 duplicated in INBOX")
	}
	if h.has("Spam", "m3") {
		t.Error("spam copied")
	}
	if got := h.st.state.LastSuccessfulHistoryID.Int64; uint64(got) != h.g.currentHistory() || got <= before {
		t.Errorf("checkpoint = %d, want %d", got, h.g.currentHistory())
	}
	if r.Session.CopiedCount != 2 { // m2, m1 (Finance); m3 skipped
		t.Errorf("copied = %d", r.Session.CopiedCount)
	}
	if r.Session.SkippedCount != 1 {
		t.Errorf("skipped = %d", r.Session.SkippedCount)
	}
}

func TestIncrementalWithNoChangesStillAdvancesTheCheckpoint(t *testing.T) {
	h := newHarness(t)
	h.g.add("m1", "INBOX")
	h.mustRun("initial", nil)
	h.g.mu.Lock()
	h.g.nextRec += 50 // the mailbox moved on without anything we care about
	h.g.mu.Unlock()
	h.mustRun("incremental", nil)
	if uint64(h.st.state.LastSuccessfulHistoryID.Int64) != h.g.currentHistory() {
		t.Errorf("checkpoint = %d, want %d: it must keep up or it will expire", h.st.state.LastSuccessfulHistoryID.Int64, h.g.currentHistory())
	}
}

func TestHistoryPagesUseIntermediateCheckpointsAndCrashLeavesThemInPlace(t *testing.T) {
	h := newHarness(t)
	h.g.add("m0", "INBOX")
	h.mustRun("initial", nil)
	for i := 1; i <= 6; i++ {
		h.g.add(fmt.Sprintf("n%d", i), "INBOX")
	}
	h.g.histPage = 2
	h.st.cfg.WorkerCount = 1
	h.st.checkpoints = nil

	ctx, crash := context.WithCancel(context.Background())
	h.d.afterEach = func(n int64) {
		if n == 1+5 { // m0 + n1..n5: crash in the middle of page 3
			crash()
		}
	}
	r := h.run(ctx, "incremental", nil)
	if r.Session.Status != StatusAbandoned {
		t.Fatalf("status = %s", r.Session.Status)
	}
	// Pages 1 and 2 were fully handled; the checkpoint is the last record of page 2
	// (never the mailbox's current historyId, which is ahead of unhandled pages).
	cp := uint64(h.st.state.LastSuccessfulHistoryID.Int64)
	if cp >= h.g.currentHistory() {
		t.Fatalf("checkpoint %d jumped past unhandled history (current %d)", cp, h.g.currentHistory())
	}
	h.d.afterEach = nil
	h.mustRun("incremental", nil)
	for i := 1; i <= 6; i++ {
		if !h.has("INBOX", fmt.Sprintf("n%d", i)) {
			t.Errorf("n%d lost", i)
		}
	}
	if h.d.count("INBOX") != 7 {
		t.Errorf("INBOX holds %d", h.d.count("INBOX"))
	}
	if uint64(h.st.state.LastSuccessfulHistoryID.Int64) != h.g.currentHistory() {
		t.Error("final checkpoint not at the current history")
	}
}

func TestExpiredHistoryReconcilesWithoutDuplicatesAndFindsMissedMessages(t *testing.T) {
	h := newHarness(t)
	h.g.add("m1", "INBOX")
	h.mustRun("initial", nil)
	h.g.addSilently("gap1", "INBOX") // arrived while we could not read the history
	h.g.addSilently("gap2", "L1")
	h.g.expiredBefore = uint64(h.st.state.LastSuccessfulHistoryID.Int64) + 1

	r := h.mustRun("incremental", nil)
	if h.st.sessType != "reconcile" {
		t.Errorf("session type = %q", h.st.sessType)
	}
	if !h.has("INBOX", "gap1") || !h.has("Projects", "gap2") {
		t.Error("messages from the gap were not recovered")
	}
	if h.d.deliveries[imapdest.HeaderValue(h.st.acct.ID.String(), "m1")+"@INBOX"] != 1 {
		t.Error("m1 duplicated by the reconcile")
	}
	if uint64(h.st.state.LastSuccessfulHistoryID.Int64) != h.g.currentHistory() {
		t.Errorf("checkpoint %d, want the current history %d", h.st.state.LastSuccessfulHistoryID.Int64, h.g.currentHistory())
	}
	if r.Session.Status != StatusCompleted {
		t.Errorf("status = %s", r.Session.Status)
	}
	found := false
	for _, e := range h.st.events {
		if e.EventType == "history_expired" {
			found = true
		}
	}
	if !found {
		t.Error("no history_expired event")
	}
}

func TestUserReconcileNeverMovesTheCheckpoint(t *testing.T) {
	h := newHarness(t)
	h.g.add("m1", "INBOX")
	h.mustRun("initial", nil)
	cp := h.st.state.LastSuccessfulHistoryID.Int64
	h.g.add("m2", "L1")
	h.g.add("m3", "L2")
	h.mustRun("reconcile", Params{LabelID: "L1"})
	if !h.has("Projects", "m2") || h.has("Finance", "m3") {
		t.Errorf("label-filtered rescan delivered the wrong set: %v", h.d.folderNames())
	}
	if h.st.state.LastSuccessfulHistoryID.Int64 != cp {
		t.Error("a user rescan must not advance the checkpoint")
	}
	// The regular incremental run still sees m3.
	h.mustRun("incremental", nil)
	if !h.has("Finance", "m3") {
		t.Error("m3 lost")
	}
}

func TestUnlabelledAndAllExcluded(t *testing.T) {
	h := newHarness(t)
	h.st.cfg.ExcludedLabels = mustJSON([]map[string]any{{"kind": "name", "value": "Newsletters"}, {"kind": "id", "value": "SPAM"}})
	h.g.add("a")                      // archive
	h.g.add("b", "L3")                // all excluded: skipped, never archived
	h.g.add("c", "L3", "L1")          // excluded + included: only Projects
	h.g.add("d", "UNREAD", "STARRED") // system labels without folders: archive
	r := h.mustRun("initial", nil)
	if !h.has("Archive", "a") || !h.has("Archive", "d") || h.has("Archive", "b") || h.has("Archive", "c") {
		t.Errorf("archive wrong: %v", h.d.folders["Archive"])
	}
	if !h.has("Projects", "c") || h.has("Newsletters", "c") || h.has("Newsletters", "b") {
		t.Error("exclusion wrong")
	}
	if r.Session.SkippedCount != 1 || r.Session.CopiedCount != 3 {
		t.Errorf("counts = copied %d skipped %d", r.Session.CopiedCount, r.Session.SkippedCount)
	}
}

func TestPartialDeliveryIsRecordedAndRetryFillsOnlyTheGap(t *testing.T) {
	h := newHarness(t)
	h.g.add("m1", "INBOX", "L1", "L2")
	h.d.failIn["Finance"] = errors.New("quota exceeded")

	r := h.mustRun("initial", nil)
	if r.Session.Status != StatusCompletedWithErrors || r.Session.FailedCount != 1 || r.Session.CopiedCount != 0 {
		t.Fatalf("session = %+v", r.Session)
	}
	// The checkpoint moved: the failure is persisted for retry, so it is handled.
	if !h.st.state.InitialSyncCompleted {
		t.Error("initial sync should complete with a recorded failure")
	}
	f := h.st.failure("m1")
	if f == nil || f.ErrorCategory != "imap_append" || f.Status != "pending" {
		t.Fatalf("failure = %+v", f)
	}
	var done, failed []string
	_ = json.Unmarshal(f.CompletedFolders, &done)
	_ = json.Unmarshal(f.FailedFolders, &failed)
	sort.Strings(done)
	if !reflect.DeepEqual(done, []string{"INBOX", "Projects"}) || !reflect.DeepEqual(failed, []string{"Finance"}) {
		t.Errorf("completed %v failed %v", done, failed)
	}
	if f.Subject != "m1" || f.Sender != "a@x.com" || f.GmailThreadID != "t-m1" {
		t.Errorf("metadata = %q %q %q", f.Subject, f.Sender, f.GmailThreadID)
	}
	if !f.NextRetryAt.Valid {
		t.Error("a retryable failure needs a retry time")
	}

	// Fix the problem and retry exactly this failure.
	delete(h.d.failIn, "Finance")
	r = h.mustRun("retry", Params{FailureIDs: []string{f.ID.String()}})
	if r.Session.RecoveredCount != 1 || r.Session.FailedCount != 0 || r.Session.Status != StatusCompleted {
		t.Fatalf("retry session = %+v", r.Session)
	}
	if h.st.failure("m1") != nil {
		t.Error("the recovered failure must be removed")
	}
	if !h.has("Finance", "m1") {
		t.Error("Finance copy missing")
	}
	for _, folder := range []string{"INBOX", "Projects", "Finance"} {
		if h.d.deliveries[imapdest.HeaderValue(h.st.acct.ID.String(), "m1")+"@"+folder] != 1 {
			t.Errorf("%s delivered %d times", folder, h.d.deliveries[imapdest.HeaderValue(h.st.acct.ID.String(), "m1")+"@"+folder])
		}
	}
}

func TestRepeatedFailureUpdatesOneRowAndCapsHistory(t *testing.T) {
	h := newHarness(t)
	h.g.add("m1", "INBOX")
	h.d.failIn["INBOX"] = errTest
	h.mustRun("initial", nil)
	id := h.st.failure("m1").ID
	for i := 0; i < 12; i++ {
		h.st.mu.Lock()
		h.st.failures["m1"].NextRetryAt = pgtype.Timestamptz{} // due now
		h.st.mu.Unlock()
		h.mustRun("incremental", nil) // retries due failures first
	}
	f := h.st.failure("m1")
	if f.ID != id || len(h.st.failures) != 1 {
		t.Fatalf("expected one stable row, got %d (id changed: %v)", len(h.st.failures), f.ID != id)
	}
	if f.AttemptCount != 10 { // the initial attempt plus nine retries, then automatic retries stop
		t.Errorf("attempts = %d", f.AttemptCount)
	}
	var hist []map[string]any
	_ = json.Unmarshal(f.ErrorHistory, &hist)
	if len(hist) != 10 {
		t.Errorf("history entries = %d, want the last 10", len(hist))
	}
	if !f.RetryExhausted || f.NextRetryAt.Valid {
		t.Errorf("after 10 attempts automatic retries must stop: %+v", f)
	}
}

func TestDueFailuresAreRetriedAtTheStartOfASession(t *testing.T) {
	h := newHarness(t)
	h.g.add("m1", "INBOX")
	h.d.failIn["INBOX"] = errTest
	h.mustRun("initial", nil)
	delete(h.d.failIn, "INBOX")

	// Not due yet: untouched.
	r := h.mustRun("incremental", nil)
	if r.Session.RecoveredCount != 0 || h.st.failure("m1") == nil {
		t.Fatal("a failure whose backoff has not passed must wait")
	}
	h.st.mu.Lock()
	h.st.failures["m1"].NextRetryAt = pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}
	h.st.mu.Unlock()
	r = h.mustRun("incremental", nil)
	if r.Session.RecoveredCount != 1 || h.st.failure("m1") != nil || !h.has("INBOX", "m1") {
		t.Errorf("recovered = %d, failure present = %v", r.Session.RecoveredCount, h.st.failure("m1") != nil)
	}
}

func TestMessageGoneFromGmailIsRecordedAsIgnored(t *testing.T) {
	h := newHarness(t)
	h.g.add("m1", "INBOX")
	h.g.add("m2", "INBOX")
	h.g.getErr["m1"] = fmt.Errorf("%w: m1", gmail.ErrMessageNotFound)
	r := h.mustRun("initial", nil)
	f := h.st.failure("m1")
	if f == nil || f.Status != "ignored" || f.ErrorCategory != "gmail_message_unavailable" {
		t.Fatalf("failure = %+v", f)
	}
	if r.Session.Status != StatusCompleted || r.Session.FailedCount != 0 || !h.has("INBOX", "m2") {
		t.Errorf("session = %+v", r.Session)
	}
	// Ignored failures are never retried automatically.
	h.g.getErr = map[string]error{}
	h.mustRun("incremental", nil)
	if h.has("INBOX", "m1") {
		t.Error("ignored failure was retried")
	}
}

func TestRevokedAuthorisationFailsTheSessionWithoutMovingAnything(t *testing.T) {
	h := newHarness(t)
	h.g.add("m1", "INBOX")
	h.g.add("m2", "INBOX")
	h.g.getErr["m1"] = fmt.Errorf("users.messages.get: %w: invalid_grant", gmail.ErrAuth)
	h.g.getErr["m2"] = h.g.getErr["m1"]
	r := h.run(context.Background(), "initial", nil)
	if !r.AuthFailed || r.Session.Status != StatusFailed {
		t.Fatalf("result = %+v", r)
	}
	if len(h.st.statuses) == 0 || h.st.statuses[0].Status != "needs_reauth" {
		t.Errorf("account statuses = %+v", h.st.statuses)
	}
	if h.st.state.InitialSyncCompleted || h.st.state.LastSuccessfulHistoryID.Valid {
		t.Error("checkpoint moved despite the auth failure")
	}
}

func TestFailureNotPersistedMeansNoCheckpoint(t *testing.T) {
	h := newHarness(t)
	h.g.add("m1", "INBOX")
	h.mustRun("initial", nil)
	h.g.add("m2", "INBOX")
	h.d.failIn["INBOX"] = errTest
	h.st.upsertErr = errors.New("database is down")
	before := h.st.state.LastSuccessfulHistoryID.Int64
	r := h.run(context.Background(), "incremental", nil)
	if r.Session.Status != StatusFailed || r.Err == nil {
		t.Fatalf("result = %+v", r)
	}
	if h.st.state.LastSuccessfulHistoryID.Int64 != before {
		t.Error("the checkpoint advanced although a failure could not be recorded")
	}
	// When the database is back the message is still found by the next run.
	h.st.upsertErr = nil
	delete(h.d.failIn, "INBOX")
	h.mustRun("incremental", nil)
	if !h.has("INBOX", "m2") {
		t.Error("m2 lost")
	}
}

func TestGracefulCancelFinishesInFlightWorkAndSavesProgress(t *testing.T) {
	h := newHarness(t)
	for i := 1; i <= 30; i++ {
		h.g.add(fmt.Sprintf("m%02d", i), "INBOX")
	}
	h.g.listPage = 5
	h.g.getDelay = 3 * time.Millisecond
	h.st.cfg.WorkerCount = 2
	h.st.cancelAfterBeats = 3
	r := h.run(context.Background(), "initial", nil)
	if r.Session.Status != StatusCancelled {
		t.Fatalf("status = %s (err %v)", r.Session.Status, r.Err)
	}
	n := h.d.count("INBOX")
	if n == 0 || n >= 30 {
		t.Fatalf("delivered %d: expected some but not all", n)
	}
	if h.st.state.InitialSyncCompleted {
		t.Error("a cancelled import is not complete")
	}
	// Resume completes without duplicates.
	h.st.cancelAfterBeats = 0
	h.mustRun("initial", nil)
	if h.d.count("INBOX") != 30 {
		t.Errorf("INBOX holds %d", h.d.count("INBOX"))
	}
}

func TestLostLeaseStopsTheRunAndLeavesTheSessionAlone(t *testing.T) {
	h := newHarness(t)
	for i := 1; i <= 50; i++ {
		h.g.add(fmt.Sprintf("m%02d", i), "INBOX")
	}
	h.g.listPage = 5
	h.g.getDelay = 3 * time.Millisecond
	h.st.cfg.WorkerCount = 1
	h.st.leaseLostAfter = 2
	r := h.run(context.Background(), "initial", nil)
	if !errors.Is(r.Err, errLeaseLost) {
		t.Fatalf("err = %v", r.Err)
	}
	if len(h.st.finished) != 0 {
		t.Error("a worker that lost its lease must not finish the session")
	}
	if h.d.count("INBOX") >= 50 {
		t.Error("work continued after the lease was lost")
	}
}

func TestUnknownLabelTriggersARefreshAndIsDeliveredToItsFolder(t *testing.T) {
	h := newHarness(t)
	h.g.add("m1", "L9") // a label created after the catalog was loaded
	callsBefore := 0
	h.g.onGet = func(id string) {
		if callsBefore == 0 {
			callsBefore++
			h.g.mu.Lock()
			h.g.labels = append(h.g.labels, gmail.Label{ID: "L9", Name: "Brand new"})
			h.g.mu.Unlock()
		}
	}
	h.mustRun("initial", nil)
	if !h.has("Brand new", "m1") || h.has("Archive", "m1") {
		t.Errorf("folders = %v", h.d.folderNames())
	}
}

func TestInvalidSettingsFailTheSessionBeforeTouchingAnything(t *testing.T) {
	h := newHarness(t)
	h.g.add("m1", "INBOX")
	h.st.cfg.ArchiveFolder = " / "
	r := h.run(context.Background(), "initial", nil)
	var ce *configError
	if !errors.As(r.Err, &ce) || r.Session.Status != StatusFailed {
		t.Fatalf("result = %+v", r)
	}
	if h.d.count("INBOX") != 0 {
		t.Error("delivered despite invalid settings")
	}
}

func TestManyMessagesConcurrently(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 300; i++ {
		h.g.add(fmt.Sprintf("m%03d", i), "INBOX", "L1")
	}
	h.g.listPage = 40
	h.st.cfg.WorkerCount = 8
	r := h.mustRun("initial", nil)
	if r.Session.CopiedCount != 300 || h.d.count("INBOX") != 300 || h.d.count("Projects") != 300 {
		t.Errorf("copied %d, INBOX %d, Projects %d", r.Session.CopiedCount, h.d.count("INBOX"), h.d.count("Projects"))
	}
	for k, n := range h.d.deliveries {
		if n != 1 {
			t.Fatalf("%s delivered %d times", k, n)
		}
	}
	if r.Session.DiscoveredCount != 300 || r.Session.ProcessedCount != 300 {
		t.Errorf("discovered %d processed %d", r.Session.DiscoveredCount, r.Session.ProcessedCount)
	}
}

func TestProgressNotificationsAreSent(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 20; i++ {
		h.g.add(fmt.Sprintf("m%d", i), "INBOX")
	}
	h.g.getDelay = time.Millisecond
	var nProgress, nFinished atomic.Int64
	e := New(h.st, Options{HeartbeatEvery: 2 * time.Millisecond, Notify: func(ev progress.Event) {
		switch ev.Kind {
		case "progress":
			nProgress.Add(1)
		case "finished":
			nFinished.Add(1)
		}
	}})
	e.Run(context.Background(), h.session("initial", nil), RunDeps{Gmail: h.g, Dest: h.d, WorkerID: "w1"})
	if nProgress.Load() == 0 || nFinished.Load() != 1 {
		t.Errorf("progress %d finished %d", nProgress.Load(), nFinished.Load())
	}
	if h.st.beats.Load() == 0 {
		t.Error("no heartbeats")
	}
}

func TestReconcileCutoffFollowsTheCheckpointNotTheLastSuccess(t *testing.T) {
	h := newHarness(t)
	h.g.add("m1", "INBOX")
	h.mustRun("initial", nil)
	// The checkpoint was read 20 days ago; a user rescan just succeeded.
	h.st.state.HistoryCheckpointAt = pgtype.Timestamptz{Time: time.Now().AddDate(0, 0, -20), Valid: true}
	h.mustRun("reconcile", Params{LabelID: "L1"})
	h.st.state.LastSuccessfulSyncAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	if h.st.state.HistoryCheckpointAt.Time.After(time.Now().AddDate(0, 0, -19)) {
		t.Fatal("a user rescan must not move the checkpoint time")
	}
	// History has expired: the reconcile must reach back to the checkpoint, not to the rescan.
	h.g.expiredBefore = uint64(h.st.state.LastSuccessfulHistoryID.Int64) + 1
	h.g.queries = nil
	h.mustRun("incremental", nil)
	want := "after:" + time.Now().AddDate(0, 0, -22).Format("2006/01/02")
	found := false
	for _, q := range h.g.queries {
		if q == want {
			found = true
		}
	}
	if !found {
		t.Errorf("queries = %v, want %q", h.g.queries, want)
	}
}

func TestLabelAddedAfterTheFirstFetchIsNotMissedOnALaterPage(t *testing.T) {
	h := newHarness(t)
	h.g.add("m0", "INBOX")
	h.mustRun("initial", nil)
	h.g.add("n1", "INBOX")
	h.g.add("n2", "INBOX")
	h.g.addLabel("n1", "L1") // recorded after n1 was added, on a later page
	h.g.histPage = 2
	h.st.cfg.WorkerCount = 1
	// n1's label arrives only after its first fetch: simulate by adding it late.
	h.g.mu.Lock()
	h.g.messages["n1"].labels = []string{"INBOX"}
	h.g.mu.Unlock()
	h.g.onGet = func(id string) {
		if id == "n2" {
			h.g.mu.Lock()
			h.g.messages["n1"].labels = []string{"INBOX", "L1"}
			h.g.mu.Unlock()
		}
	}
	h.mustRun("incremental", nil)
	if !h.has("Projects", "n1") {
		t.Errorf("the copy for the later label was missed: %v", h.d.folderNames())
	}
	if h.d.deliveries[imapdest.HeaderValue(h.st.acct.ID.String(), "n1")+"@INBOX"] != 1 {
		t.Error("INBOX copy duplicated")
	}
}

func TestPendingFailureIsResolvedWhenTheMessageArrivesByAnotherRoute(t *testing.T) {
	h := newHarness(t)
	h.g.add("m1", "INBOX")
	h.d.failIn["INBOX"] = errTest
	h.mustRun("initial", nil)
	if h.st.failure("m1") == nil {
		t.Fatal("no failure recorded")
	}
	delete(h.d.failIn, "INBOX")
	// Not due, not retried: a rescan delivers it instead.
	h.mustRun("reconcile", Params{After: "2000-01-01"})
	if h.st.failure("m1") != nil || !h.has("INBOX", "m1") {
		t.Error("the failure row must be resolved once the message is delivered")
	}
}

func TestManuallyRequestedRetryIncludesPermanentCategories(t *testing.T) {
	h := newHarness(t)
	h.g.add("m1", "INBOX")
	h.d.failIn["INBOX"] = errTest
	h.mustRun("initial", nil)
	h.st.mu.Lock()
	h.st.failures["m1"].ErrorCategory = "imap_auth"
	h.st.mu.Unlock()
	delete(h.d.failIn, "INBOX")
	r := h.mustRun("incremental", nil)
	if r.Session.RecoveredCount != 0 {
		t.Fatal("a permanent failure must not be retried automatically")
	}
	h.st.mu.Lock()
	h.st.failures["m1"].RetryRequested = true // what ResetFailureForRetry does
	h.st.failures["m1"].NextRetryAt = pgtype.Timestamptz{}
	h.st.mu.Unlock()
	r = h.mustRun("incremental", nil)
	if r.Session.RecoveredCount != 1 || !h.has("INBOX", "m1") {
		t.Errorf("requested retry: %+v", r.Session)
	}
}

func TestPersistentHeartbeatFailureStopsTheRunBeforeTheLeaseCanBeTakenOver(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 200; i++ {
		h.g.add(fmt.Sprintf("m%03d", i), "INBOX")
	}
	h.g.listPage = 5
	h.g.getDelay = 8 * time.Millisecond
	h.st.cfg.WorkerCount = 1
	h.st.beatErr = errors.New("database unreachable")
	e := New(h.st, Options{HeartbeatEvery: time.Millisecond, LeaseSeconds: 1, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}) // limit: 2/3 s
	r := e.Run(context.Background(), h.session("initial", nil), RunDeps{Gmail: h.g, Dest: h.d, WorkerID: "w1"})
	if !errors.Is(r.Err, errLeaseLost) {
		t.Fatalf("err = %v", r.Err)
	}
	if h.d.count("INBOX") >= 200 {
		t.Error("kept delivering without a valid lease")
	}
	if len(h.st.finished) != 0 {
		t.Error("must not finish a session it may no longer own")
	}
}

func TestReauthMessageNamesTheTestingExpiry(t *testing.T) {
	now := time.Date(2025, 1, 20, 0, 0, 0, 0, time.UTC)
	at := func(d time.Duration) pgtype.Timestamptz { return pgtype.Timestamptz{Time: now.Add(-d), Valid: true} }
	cases := []struct {
		name string
		at   pgtype.Timestamptz
		want string
	}{
		{"unknown age", pgtype.Timestamptz{}, "revoked or expired"},
		{"seven days", at(7*24*time.Hour + 3*time.Hour), `"Testing"`},
		{"six and a half days", at(6*24*time.Hour + 12*time.Hour), `"Testing"`},
		{"hours", at(5 * time.Hour), "within a day"},
		{"months", at(90 * 24 * time.Hour), "password"},
		{"three days", at(3 * 24 * time.Hour), "3 days"},
	}
	for _, tc := range cases {
		if got := ReauthMessage(tc.at, now); !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q lacks %q", tc.name, got, tc.want)
		}
	}
}

func TestRateLimitPacingIsReportedAndRemembered(t *testing.T) {
	h := newHarness(t)
	h.g.add("m1", "INBOX")
	h.g.hits = 3
	h.mustRun("initial", nil)
	found := false
	for _, e := range h.st.events {
		if e.EventType == "rate_limited" && strings.Contains(e.Message, "3 times") {
			found = true
		}
	}
	if !found {
		t.Errorf("events = %+v", h.st.events)
	}
	if !h.st.learned.Valid || h.st.learned.Float32 != 0.25 {
		t.Errorf("learned = %+v", h.st.learned)
	}
	// A quiet session records nothing new.
	h2 := newHarness(t)
	h2.g.add("m1", "INBOX")
	h2.mustRun("initial", nil)
	if h2.st.learned.Valid {
		t.Error("nothing was learned in a quiet session")
	}
}
