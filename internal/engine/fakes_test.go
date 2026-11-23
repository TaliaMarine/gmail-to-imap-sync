package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/gmail"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/imapdest"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/mapping"
)

// ---- fake store ----------------------------------------------------------

type fakeStore struct {
	mu sync.Mutex

	acct     store.Account
	cfg      store.AccountSyncSetting
	state    store.SyncState
	labels   map[string]store.UpsertLabelParams
	failures map[string]*store.FailedMessage // by gmail id
	events   []store.InsertEventParams
	finished []store.FinishSessionParams
	statuses []store.SetAccountStatusParams

	sessType string
	beats    atomic.Int64

	// hooks
	cancelAfterBeats int64 // >0: HeartbeatSession reports a cancel request after that many beats
	leaseLostAfter   int64 // >0: HeartbeatSession reports no row after that many beats
	upsertErr        error
	beatErr          error // returned by every heartbeat
	checkpoints      []int64
	learned          pgtype.Float4
}

func newFakeStore() *fakeStore {
	id := uuid.New()
	fs := &fakeStore{
		acct:     store.Account{ID: id, Name: "acct", Status: "ok"},
		labels:   map[string]store.UpsertLabelParams{},
		failures: map[string]*store.FailedMessage{},
		state:    store.SyncState{AccountID: id},
	}
	fs.cfg = defaultRow(id)
	return fs
}

func defaultRow(id uuid.UUID) store.AccountSyncSetting {
	d := mapping.DefaultSettings()
	return store.AccountSyncSetting{
		AccountID: id, Enabled: true, ScheduleIntervalSeconds: 7200,
		ArchiveFolder: d.ArchiveFolder, SentFolder: d.SentFolder, InboxFolder: d.InboxFolder,
		ExcludedLabels: mustJSON(d.Excluded), LabelMapping: []byte(`{}`),
		FlagMapping:      []byte(`{"unread_as_seen":true,"starred_as_flagged":true,"folder_labels":[]}`),
		SubscribeFolders: true, WorkerCount: 4, RateFraction: 0.6, BatchSize: 100, RetryMaxAttempts: 10,
	}
}

func (f *fakeStore) GetAccount(context.Context, uuid.UUID) (store.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acct, nil
}
func (f *fakeStore) GetSyncSettings(context.Context, uuid.UUID) (store.AccountSyncSetting, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg, nil
}
func (f *fakeStore) GetSyncState(context.Context, uuid.UUID) (store.SyncState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, nil
}
func (f *fakeStore) EnsureSyncState(context.Context, uuid.UUID) error { return nil }
func (f *fakeStore) BeginInitialSync(_ context.Context, a store.BeginInitialSyncParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.state.StartHistoryID.Valid {
		f.state.StartHistoryID = a.StartHistoryID
		f.state.StartHistoryAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	}
	return nil
}
func (f *fakeStore) SaveInitialCursor(_ context.Context, a store.SaveInitialCursorParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.InitialSyncCursor = a.InitialSyncCursor
	return nil
}
func (f *fakeStore) ResetInitialSync(context.Context, uuid.UUID) error { return nil }
func (f *fakeStore) CompleteInitialSync(context.Context, uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.InitialSyncCompleted = true
	f.state.InitialSyncCursor = nil
	f.state.LastSuccessfulHistoryID = f.state.StartHistoryID
	f.state.LastSuccessfulSyncAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	f.state.HistoryCheckpointAt = f.state.StartHistoryAt
	return nil
}
func (f *fakeStore) SaveCheckpoint(_ context.Context, a store.SaveCheckpointParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkpoints = append(f.checkpoints, a.LastSuccessfulHistoryID.Int64)
	if !f.state.LastSuccessfulHistoryID.Valid || f.state.LastSuccessfulHistoryID.Int64 <= a.LastSuccessfulHistoryID.Int64 {
		f.state.LastSuccessfulHistoryID = a.LastSuccessfulHistoryID
		f.state.LastSuccessfulSyncAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	}
	return nil
}
func (f *fakeStore) SaveFinalCheckpoint(_ context.Context, a store.SaveFinalCheckpointParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkpoints = append(f.checkpoints, a.HistoryID.Int64)
	if !f.state.LastSuccessfulHistoryID.Valid || f.state.LastSuccessfulHistoryID.Int64 <= a.HistoryID.Int64 {
		f.state.LastSuccessfulHistoryID = a.HistoryID
		f.state.HistoryCheckpointAt = a.At
		f.state.LastSuccessfulSyncAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	}
	return nil
}
func (f *fakeStore) PendingFailureKeys(_ context.Context, _ uuid.UUID) ([]store.PendingFailureKeysRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.PendingFailureKeysRow
	for _, r := range f.failures {
		if r.Status == "pending" {
			out = append(out, store.PendingFailureKeysRow{ID: r.ID, GmailMessageID: r.GmailMessageID})
		}
	}
	return out, nil
}
func (f *fakeStore) SetLearnedRate(_ context.Context, a store.SetLearnedRateParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.learned = a.LearnedRateFraction
	return nil
}
func (f *fakeStore) TouchLastSuccess(context.Context, uuid.UUID) error { return nil }
func (f *fakeStore) UpsertLabel(_ context.Context, a store.UpsertLabelParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.labels[a.LabelID] = a
	return nil
}
func (f *fakeStore) SetAccountStatus(_ context.Context, a store.SetAccountStatusParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, a)
	f.acct.Status = a.Status
	return nil
}

func (f *fakeStore) HeartbeatSession(context.Context, store.HeartbeatSessionParams) (pgtype.Timestamptz, error) {
	n := f.beats.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beatErr != nil {
		return pgtype.Timestamptz{}, f.beatErr
	}
	if f.leaseLostAfter > 0 && n > f.leaseLostAfter {
		return pgtype.Timestamptz{}, pgx.ErrNoRows
	}
	if f.cancelAfterBeats > 0 && n > f.cancelAfterBeats {
		return pgtype.Timestamptz{Time: time.Now(), Valid: true}, nil
	}
	return pgtype.Timestamptz{}, nil
}
func (f *fakeStore) SetSessionStartingHistory(context.Context, store.SetSessionStartingHistoryParams) error {
	return nil
}
func (f *fakeStore) SetSessionType(_ context.Context, a store.SetSessionTypeParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessType = a.SyncType
	return nil
}
func (f *fakeStore) FinishSession(_ context.Context, a store.FinishSessionParams) (store.SyncSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finished = append(f.finished, a)
	return store.SyncSession{ID: a.ID, Status: a.Status, LastError: a.LastError, CopiedCount: a.Copied, SkippedCount: a.Skipped,
		FailedCount: a.Failed, RecoveredCount: a.Recovered, ProcessedCount: a.Processed, DiscoveredCount: a.Discovered}, nil
}
func (f *fakeStore) InsertEvent(_ context.Context, a store.InsertEventParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, a)
	return nil
}

func (f *fakeStore) UpsertFailure(_ context.Context, a store.UpsertFailureParams) (store.FailedMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.upsertErr != nil {
		return store.FailedMessage{}, f.upsertErr
	}
	row, ok := f.failures[a.GmailMessageID]
	if !ok {
		row = &store.FailedMessage{ID: a.ID, AccountID: a.AccountID, GmailMessageID: a.GmailMessageID, Status: "pending"}
	}
	row.AttemptCount++
	row.GmailThreadID, row.Subject, row.Sender, row.Recipients, row.MessageDate = a.GmailThreadID, a.Subject, a.Sender, a.Recipients, a.MessageDate
	row.IntendedFolders, row.CompletedFolders, row.FailedFolders = a.IntendedFolders, a.CompletedFolders, a.FailedFolders
	row.ErrorCategory, row.ErrorMessage, row.ErrorHistory = a.ErrorCategory, a.ErrorMessage, a.ErrorHistory
	row.RetryExhausted, row.NextRetryAt, row.LastSessionID = a.RetryExhausted, a.NextRetryAt, a.LastSessionID
	f.failures[a.GmailMessageID] = row
	return *row, nil
}
func (f *fakeStore) GetFailureByMessage(_ context.Context, a store.GetFailureByMessageParams) (store.FailedMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if row, ok := f.failures[a.GmailMessageID]; ok {
		return *row, nil
	}
	return store.FailedMessage{}, pgx.ErrNoRows
}
func (f *fakeStore) DueFailures(_ context.Context, a store.DueFailuresParams) ([]store.FailedMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.FailedMessage
	for _, r := range f.failures {
		due := !r.NextRetryAt.Valid || !r.NextRetryAt.Time.After(time.Now())
		perm := map[string]bool{"gmail_auth": true, "imap_auth": true, "config": true, "parse": true, "gmail_message_unavailable": true}
		if r.Status == "pending" && !r.RetryExhausted && (r.RetryRequested || !perm[r.ErrorCategory]) && due {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GmailMessageID < out[j].GmailMessageID })
	return out, nil
}
func (f *fakeStore) FailuresByIDs(_ context.Context, ids []uuid.UUID) ([]store.FailedMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.FailedMessage
	for _, r := range f.failures {
		for _, id := range ids {
			if r.ID == id {
				out = append(out, *r)
			}
		}
	}
	return out, nil
}
func (f *fakeStore) ResolveFailure(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, r := range f.failures {
		if r.ID == id {
			delete(f.failures, k)
		}
	}
	return nil
}
func (f *fakeStore) SetFailureStatus(_ context.Context, a store.SetFailureStatusParams) (store.FailedMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.failures {
		if r.ID == a.ID {
			r.Status = a.Status
			return *r, nil
		}
	}
	return store.FailedMessage{}, pgx.ErrNoRows
}

func (f *fakeStore) failure(gmailID string) *store.FailedMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.failures[gmailID]; ok {
		c := *r
		return &c
	}
	return nil
}

func (f *fakeStore) lastFinish() store.FinishSessionParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.finished) == 0 {
		return store.FinishSessionParams{}
	}
	return f.finished[len(f.finished)-1]
}

// ---- fake gmail ----------------------------------------------------------

type fakeMessage struct {
	id     string
	labels []string
	raw    []byte
	date   time.Time
}

type histEvent struct {
	rec        uint64
	messageID  string
	labelAdded []string // non-empty: a labelsAdded event
}

type fakeGmail struct {
	mu       sync.Mutex
	messages map[string]*fakeMessage
	order    []string
	history  []histEvent
	nextRec  uint64
	labels   []gmail.Label

	listPage, histPage int
	expiredBefore      uint64 // start history IDs below this are expired
	getErr             map[string]error
	getDelay           time.Duration
	getCalls           map[string]int
	profileCalls       int
	reqs               atomic.Int64
	onGet              func(id string)
	labelCalls         int
	queries            []string
	hits               int
}

func newFakeGmail() *fakeGmail {
	return &fakeGmail{
		messages: map[string]*fakeMessage{}, getErr: map[string]error{}, getCalls: map[string]int{},
		nextRec: 100, listPage: 500, histPage: 500,
		labels: []gmail.Label{
			{ID: "INBOX", Name: "INBOX", System: true}, {ID: "SENT", Name: "SENT", System: true},
			{ID: "SPAM", Name: "SPAM", System: true}, {ID: "UNREAD", Name: "UNREAD", System: true},
			{ID: "STARRED", Name: "STARRED", System: true},
			{ID: "L1", Name: "Projects"}, {ID: "L2", Name: "Finance"}, {ID: "L3", Name: "Newsletters"},
		},
	}
}

// add stores a message and records a messageAdded history event.
func (g *fakeGmail) add(id string, labels ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.messages[id] = &fakeMessage{id: id, labels: labels, date: time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
		raw: []byte("From: a@x.com\r\nSubject: " + id + "\r\nMessage-ID: <" + id + "@x>\r\n\r\nbody " + id + "\r\n")}
	g.order = append(g.order, id)
	g.nextRec++
	g.history = append(g.history, histEvent{rec: g.nextRec, messageID: id})
}

// addLabel adds a label to an existing message and records a labelsAdded event.
func (g *fakeGmail) addLabel(id, label string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.messages[id].labels = append(g.messages[id].labels, label)
	g.nextRec++
	g.history = append(g.history, histEvent{rec: g.nextRec, messageID: id, labelAdded: []string{label}})
}

// addSilently adds a message with no history event (it happened during a gap
// in the history, as after an expired checkpoint).
func (g *fakeGmail) addSilently(id string, labels ...string) {
	g.add(id, labels...)
	g.mu.Lock()
	g.history = g.history[:len(g.history)-1]
	g.mu.Unlock()
}

func (g *fakeGmail) currentHistory() uint64 { g.mu.Lock(); defer g.mu.Unlock(); return g.nextRec }

func (g *fakeGmail) Requests() int64 { return g.reqs.Load() }

// Pacing makes the fake report rate limiting when hits is set.
func (g *fakeGmail) Pacing() (int, time.Duration, float64, float64) {
	return g.hits, time.Duration(g.hits) * 20 * time.Second, 0.2, 0.25
}

func (g *fakeGmail) Profile(context.Context) (gmail.Profile, error) {
	g.reqs.Add(1)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.profileCalls++
	return gmail.Profile{Email: "me@gmail.com", HistoryID: g.nextRec}, nil
}

func (g *fakeGmail) Labels(context.Context) ([]gmail.Label, error) {
	g.reqs.Add(1)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.labelCalls++
	return append([]gmail.Label(nil), g.labels...), nil
}

func (g *fakeGmail) ListMessages(_ context.Context, o gmail.ListOptions) (gmail.ListPage, error) {
	g.reqs.Add(1)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.queries = append(g.queries, o.Query)
	var matching []string
	for _, id := range g.order {
		m := g.messages[id]
		if len(o.LabelIDs) > 0 && !hasAll(m.labels, o.LabelIDs) {
			continue
		}
		matching = append(matching, id)
	}
	start := 0
	if o.PageToken != "" {
		start, _ = strconv.Atoi(o.PageToken)
	}
	size := g.listPage
	if o.PageSize > 0 && int(o.PageSize) < size {
		size = int(o.PageSize)
	}
	end := start + size
	if end > len(matching) {
		end = len(matching)
	}
	page := gmail.ListPage{}
	for _, id := range matching[start:end] {
		page.Messages = append(page.Messages, gmail.MessageRef{ID: id})
	}
	if end < len(matching) {
		page.NextPageToken = strconv.Itoa(end)
	}
	return page, nil
}

func hasAll(have, want []string) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			if h == w {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (g *fakeGmail) GetRaw(ctx context.Context, id string) (*gmail.Message, error) {
	g.reqs.Add(1)
	if g.onGet != nil {
		g.onGet(id)
	}
	if g.getDelay > 0 {
		select {
		case <-time.After(g.getDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.getCalls[id]++
	if err, ok := g.getErr[id]; ok && err != nil {
		return nil, err
	}
	m, ok := g.messages[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", gmail.ErrMessageNotFound, id)
	}
	return &gmail.Message{ID: id, ThreadID: "t-" + id, LabelIDs: append([]string(nil), m.labels...),
		InternalDate: m.date, Raw: append([]byte(nil), m.raw...)}, nil
}

func (g *fakeGmail) History(_ context.Context, start uint64, token string) (gmail.HistoryPage, error) {
	g.reqs.Add(1)
	g.mu.Lock()
	defer g.mu.Unlock()
	if start < g.expiredBefore {
		return gmail.HistoryPage{}, fmt.Errorf("%w (start %d)", gmail.ErrHistoryExpired, start)
	}
	var evs []histEvent
	for _, e := range g.history {
		if e.rec > start { // "after" the given ID
			evs = append(evs, e)
		}
	}
	from := 0
	if token != "" {
		from, _ = strconv.Atoi(token)
	}
	to := from + g.histPage
	if to > len(evs) {
		to = len(evs)
	}
	page := gmail.HistoryPage{HistoryID: g.nextRec}
	for _, e := range evs[from:to] {
		page.LastRecordID = e.rec
		if len(e.labelAdded) > 0 {
			page.LabelsAdded = append(page.LabelsAdded, gmail.LabelAdded{MessageID: e.messageID, LabelIDs: e.labelAdded})
		} else {
			page.MessagesAdded = append(page.MessagesAdded, gmail.MessageRef{ID: e.messageID})
		}
	}
	if to < len(evs) {
		page.NextPageToken = strconv.Itoa(to)
	}
	return page, nil
}

// ---- fake destination ----------------------------------------------------

type fakeDest struct {
	mu         sync.Mutex
	folders    map[string]map[string]imapdest.Message // folder -> archive id -> message
	failIn     map[string]error                       // folder -> error returned by Deliver
	created    int64
	delivered  int64
	bytes      int64
	afterEach  func(n int64) // called after the n-th successful delivery
	deliveries map[string]int
}

func newFakeDest() *fakeDest {
	return &fakeDest{folders: map[string]map[string]imapdest.Message{"INBOX": {}}, failIn: map[string]error{}, deliveries: map[string]int{}}
}

func (d *fakeDest) Delimiter() string { return "/" }

func (d *fakeDest) Stats() imapdest.Stats {
	d.mu.Lock()
	defer d.mu.Unlock()
	return imapdest.Stats{FoldersCreated: d.created, Delivered: d.delivered, BytesDelivered: d.bytes}
}

func (d *fakeDest) Deliver(ctx context.Context, m imapdest.Message, folders []string) []imapdest.Result {
	var out []imapdest.Result
	for _, f := range folders {
		if err := ctx.Err(); err != nil {
			out = append(out, imapdest.Result{Folder: f, Status: imapdest.Failed, Err: err})
			continue
		}
		d.mu.Lock()
		if err := d.failIn[f]; err != nil {
			d.mu.Unlock()
			out = append(out, imapdest.Result{Folder: f, Status: imapdest.Failed, Err: &imapdest.OpError{Kind: imapdest.KindAppend, Op: "append", Folder: f, Err: err}})
			continue
		}
		fo, ok := d.folders[f]
		created := false
		if !ok {
			fo = map[string]imapdest.Message{}
			d.folders[f] = fo
			d.created++
			created = true
		}
		if _, dup := fo[m.ID]; dup {
			d.mu.Unlock()
			out = append(out, imapdest.Result{Folder: f, Status: imapdest.Skipped})
			continue
		}
		fo[m.ID] = m
		d.delivered++
		d.bytes += int64(len(m.Raw))
		d.deliveries[m.ID+"@"+f]++
		n := d.delivered
		cb := d.afterEach
		d.mu.Unlock()
		out = append(out, imapdest.Result{Folder: f, Status: imapdest.Delivered, Created: created})
		if cb != nil {
			cb(n)
		}
	}
	return out
}

// has reports whether folder holds the Gmail message id of account.
func (d *fakeDest) has(account uuid.UUID, folder, gmailID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.folders[folder][imapdest.HeaderValue(account.String(), gmailID)]
	return ok
}

func (d *fakeDest) count(folder string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.folders[folder])
}

func (d *fakeDest) folderNames() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for n := range d.folders {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

var errTest = errors.New("injected failure")
