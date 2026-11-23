//go:build integration

package jobs_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/clients"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/crypto"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/gmail"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/imapdest"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/jobs"
)

var logger = slog.New(slog.NewTextHandler(io.Discard, nil))

func newDB(t *testing.T) *db.DB {
	t.Helper()
	ctx := context.Background()
	pg, err := postgres.Run(ctx, "postgres:17-alpine", postgres.WithDatabase("j"), postgres.WithUsername("j"),
		postgres.WithPassword("j"), postgres.BasicWaitStrategies())
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	url, _ := pg.ConnectionString(ctx, "sslmode=disable")
	d, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	if err := d.Migrate(); err != nil {
		t.Fatal(err)
	}
	return d
}

// seedAccount creates a destination, an authorised account and its settings.
func seedAccount(t *testing.T, d *db.DB, name string, enabled bool, interval int32) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	dest, err := d.CreateDestination(ctx, store.CreateDestinationParams{ID: uuid.New(), Name: name + "-dest", Host: "h", Port: 993,
		TlsMode: "implicit", Username: "u", EncPassword: "x", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	acct, err := d.CreateAccount(ctx, store.CreateAccountParams{ID: uuid.New(), Name: name, DestinationID: uuid.NullUUID{UUID: dest.ID, Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetAccountAuthorised(ctx, store.SetAccountAuthorisedParams{ID: acct.ID, EncRefreshToken: "tok", GmailAddress: name + "@gmail.com"}); err != nil {
		t.Fatal(err)
	}
	if err := d.EnsureSyncSettings(ctx, acct.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.EnsureSyncState(ctx, acct.ID); err != nil {
		t.Fatal(err)
	}
	cfg, _ := d.GetSyncSettings(ctx, acct.ID)
	if _, err := d.UpdateSyncSettings(ctx, store.UpdateSyncSettingsParams{
		AccountID: acct.ID, Enabled: enabled, ScheduleIntervalSeconds: interval,
		ArchiveFolder: cfg.ArchiveFolder, SentFolder: cfg.SentFolder, InboxFolder: cfg.InboxFolder,
		ExcludedLabels: cfg.ExcludedLabels, LabelMapping: cfg.LabelMapping, FlagMapping: cfg.FlagMapping,
		SubscribeFolders: true, WorkerCount: 4, RateFraction: 0.6, BatchSize: 100, RetryMaxAttempts: 10,
	}); err != nil {
		t.Fatal(err)
	}
	return acct.ID
}

func enqueue(t *testing.T, d *db.DB, account uuid.UUID, syncType string) store.SyncSession {
	t.Helper()
	s, err := d.CreateSession(context.Background(), store.CreateSessionParams{ID: uuid.New(), AccountID: account,
		TriggerType: "manual", SyncType: syncType, Params: []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func session(t *testing.T, d *db.DB, id uuid.UUID) store.SyncSession {
	s, err := d.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// ---- fakes -----------------------------------------------------------------

type fakeGmail struct {
	n     int
	delay time.Duration
	err   error
	reqs  atomic.Int64
}

func (g *fakeGmail) Requests() int64 { return g.reqs.Load() }
func (g *fakeGmail) Profile(context.Context) (gmail.Profile, error) {
	return gmail.Profile{Email: "x@gmail.com", HistoryID: 500}, nil
}
func (g *fakeGmail) Labels(context.Context) ([]gmail.Label, error) {
	return []gmail.Label{{ID: "INBOX", Name: "INBOX", System: true}}, nil
}
func (g *fakeGmail) ListMessages(_ context.Context, o gmail.ListOptions) (gmail.ListPage, error) {
	var p gmail.ListPage
	for i := 0; i < g.n; i++ {
		p.Messages = append(p.Messages, gmail.MessageRef{ID: fmt.Sprintf("m%03d", i)})
	}
	return p, nil
}
func (g *fakeGmail) GetRaw(ctx context.Context, id string) (*gmail.Message, error) {
	g.reqs.Add(1)
	if g.err != nil {
		return nil, g.err
	}
	if g.delay > 0 {
		select {
		case <-time.After(g.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &gmail.Message{ID: id, LabelIDs: []string{"INBOX"}, InternalDate: time.Now(), Raw: []byte("Subject: " + id + "\r\n\r\nbody")}, nil
}
func (g *fakeGmail) History(context.Context, uint64, string) (gmail.HistoryPage, error) {
	return gmail.HistoryPage{HistoryID: 600}, nil
}

type fakeDest struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (d *fakeDest) Delimiter() string { return "/" }
func (d *fakeDest) Stats() imapdest.Stats {
	d.mu.Lock()
	defer d.mu.Unlock()
	return imapdest.Stats{Delivered: int64(len(d.seen))}
}
func (d *fakeDest) Deliver(_ context.Context, m imapdest.Message, folders []string) []imapdest.Result {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []imapdest.Result
	for _, f := range folders {
		k := f + "|" + m.ID
		if d.seen[k] {
			out = append(out, imapdest.Result{Folder: f, Status: imapdest.Skipped})
			continue
		}
		d.seen[k] = true
		out = append(out, imapdest.Result{Folder: f, Status: imapdest.Delivered})
	}
	return out
}

// builder serves fakes and counts builds per session.
type builder struct {
	mu     sync.Mutex
	builds map[uuid.UUID]int
	gm     func(store.SyncSession) *fakeGmail
	dest   *fakeDest
	err    error
}

func newBuilder(gm func(store.SyncSession) *fakeGmail) *builder {
	return &builder{builds: map[uuid.UUID]int{}, gm: gm, dest: &fakeDest{seen: map[string]bool{}}}
}

func (b *builder) Build(_ context.Context, s store.SyncSession) (jobs.Resources, error) {
	b.mu.Lock()
	b.builds[s.ID]++
	b.mu.Unlock()
	if b.err != nil {
		return jobs.Resources{}, b.err
	}
	return jobs.Resources{Gmail: b.gm(s), Dest: b.dest}, nil
}

func startRunner(t *testing.T, d *db.DB, b jobs.Builder, mut func(*jobs.Options)) (stop func()) {
	t.Helper()
	o := jobs.Options{DB: d, Builder: b, Logger: logger, PollEvery: 20 * time.Millisecond, ScheduleEvery: 50 * time.Millisecond,
		HeartbeatEvery: 10 * time.Millisecond, LeaseSeconds: 30}
	if mut != nil {
		mut(&o)
	}
	r := jobs.NewRunner(o)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); r.Run(ctx) }()
	go func() { defer wg.Done(); r.Maintain(ctx) }()
	var once sync.Once
	stop = func() { once.Do(func() { cancel(); wg.Wait() }) }
	t.Cleanup(stop)
	return stop
}

// ---- tests -----------------------------------------------------------------

func TestQueuedSessionRunsToCompletionAgainstRealDatabase(t *testing.T) {
	d := newDB(t)
	acct := seedAccount(t, d, "a", false, 7200)
	b := newBuilder(func(store.SyncSession) *fakeGmail { return &fakeGmail{n: 25} })
	startRunner(t, d, b, nil)

	s := enqueue(t, d, acct, "initial")
	waitFor(t, "session to complete", 10*time.Second, func() bool { return session(t, d, s.ID).Status == "completed" })

	got := session(t, d, s.ID)
	if got.CopiedCount != 25 || got.DiscoveredCount != 25 || got.FailedCount != 0 || !got.DurationMs.Valid || got.LeaseUntil.Valid {
		t.Errorf("session = %+v", got)
	}
	st, _ := d.GetSyncState(context.Background(), acct)
	if !st.InitialSyncCompleted || st.LastSuccessfulHistoryID.Int64 != 500 {
		t.Errorf("state = %+v", st)
	}
	events, _ := d.ListEvents(context.Background(), store.ListEventsParams{SessionID: s.ID, ID: 0, Limit: 100})
	if len(events) < 2 {
		t.Errorf("events = %d", len(events))
	}
	labels, _ := d.ListLabels(context.Background(), acct)
	if len(labels) != 1 {
		t.Errorf("labels = %d", len(labels))
	}
	// The account may run again (the active-session slot is free).
	enqueue(t, d, acct, "incremental")
}

func TestTwoRunnersNeverRunTheSameSessionTwice(t *testing.T) {
	d := newDB(t)
	var ids []uuid.UUID
	b := newBuilder(func(store.SyncSession) *fakeGmail { return &fakeGmail{n: 5, delay: 2 * time.Millisecond} })
	for i := 0; i < 6; i++ {
		a := seedAccount(t, d, fmt.Sprintf("acc%d", i), false, 7200)
		ids = append(ids, enqueue(t, d, a, "initial").ID)
	}
	startRunner(t, d, b, func(o *jobs.Options) { o.WorkerID = "pod-a"; o.MaxConcurrent = 2 })
	startRunner(t, d, b, func(o *jobs.Options) { o.WorkerID = "pod-b"; o.MaxConcurrent = 2 })

	waitFor(t, "all sessions to complete", 20*time.Second, func() bool {
		for _, id := range ids {
			if session(t, d, id).Status != "completed" {
				return false
			}
		}
		return true
	})
	for _, id := range ids {
		if n := b.builds[id]; n != 1 {
			t.Errorf("session %s built %d times", id, n)
		}
	}
}

func TestOneActiveSessionPerAccountIsEnforced(t *testing.T) {
	d := newDB(t)
	acct := seedAccount(t, d, "a", false, 7200)
	enqueue(t, d, acct, "incremental")
	if _, err := d.CreateSession(context.Background(), store.CreateSessionParams{ID: uuid.New(), AccountID: acct,
		TriggerType: "manual", SyncType: "incremental", Params: []byte("{}")}); err == nil {
		t.Fatal("a second queued session for the same account was accepted")
	}
}

func TestExpiredLeaseIsAbandonedAndRecovered(t *testing.T) {
	d := newDB(t)
	acct := seedAccount(t, d, "a", false, 7200)
	s := enqueue(t, d, acct, "incremental")
	ctx := context.Background()
	// A worker claimed it and then died: running, lease in the past.
	if _, err := d.Pool.Exec(ctx, `UPDATE sync_sessions SET status='running', started_at=now(), worker_id='dead', lease_until=now() - interval '1 minute' WHERE id=$1`, s.ID); err != nil {
		t.Fatal(err)
	}
	b := newBuilder(func(store.SyncSession) *fakeGmail { return &fakeGmail{n: 0} })
	startRunner(t, d, b, nil)

	waitFor(t, "abandoned session", 10*time.Second, func() bool { return session(t, d, s.ID).Status == "abandoned" })
	// A recovery session was queued and ran.
	waitFor(t, "recovery session", 10*time.Second, func() bool {
		rows, _ := d.ListSessions(ctx, store.ListSessionsParams{AccountID: uuid.NullUUID{UUID: acct, Valid: true}, Lim: 10})
		for _, r := range rows {
			if r.TriggerType == "recovery" && (r.Status == "completed" || r.Status == "completed_with_errors") {
				return true
			}
		}
		return false
	})
}

func TestSchedulerEnqueuesDueAccountsOnce(t *testing.T) {
	d := newDB(t)
	on := seedAccount(t, d, "on", true, 300)
	off := seedAccount(t, d, "off", false, 300)
	b := newBuilder(func(store.SyncSession) *fakeGmail { return &fakeGmail{n: 3} })
	startRunner(t, d, b, nil)

	ctx := context.Background()
	waitFor(t, "scheduled initial session", 10*time.Second, func() bool {
		rows, _ := d.ListSessions(ctx, store.ListSessionsParams{AccountID: uuid.NullUUID{UUID: on, Valid: true}, Lim: 10})
		return len(rows) == 1 && rows[0].Status == "completed" && rows[0].TriggerType == "initial" && rows[0].SyncType == "initial"
	})
	time.Sleep(400 * time.Millisecond) // several ticks: the 5 minute interval has not passed
	rows, _ := d.ListSessions(ctx, store.ListSessionsParams{AccountID: uuid.NullUUID{UUID: on, Valid: true}, Lim: 10})
	if len(rows) != 1 {
		t.Errorf("the scheduler enqueued %d sessions, want 1", len(rows))
	}
	if rows, _ := d.ListSessions(ctx, store.ListSessionsParams{AccountID: uuid.NullUUID{UUID: off, Valid: true}, Lim: 10}); len(rows) != 0 {
		t.Error("a disabled account was scheduled")
	}
	// Once the interval has passed it runs again, now as a scheduled incremental.
	if _, err := d.Pool.Exec(ctx, `UPDATE sync_sessions SET created_at = now() - interval '1 hour' WHERE account_id=$1`, on); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second scheduled session", 10*time.Second, func() bool {
		rows, _ := d.ListSessions(ctx, store.ListSessionsParams{AccountID: uuid.NullUUID{UUID: on, Valid: true}, Lim: 10})
		return len(rows) == 2 && rows[0].TriggerType == "scheduled" && rows[0].Status == "completed"
	})
}

func TestCancelStopsARunningSession(t *testing.T) {
	d := newDB(t)
	acct := seedAccount(t, d, "a", false, 7200)
	b := newBuilder(func(store.SyncSession) *fakeGmail { return &fakeGmail{n: 400, delay: 5 * time.Millisecond} })
	startRunner(t, d, b, nil)
	s := enqueue(t, d, acct, "initial")
	waitFor(t, "session to start copying", 10*time.Second, func() bool {
		x := session(t, d, s.ID)
		return x.Status == "running" && x.ProcessedCount > 0
	})
	if _, err := d.RequestSessionCancel(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "cancelled", 10*time.Second, func() bool { return session(t, d, s.ID).Status == "cancelled" })
	if got := session(t, d, s.ID); got.ProcessedCount >= 400 || got.ProcessedCount == 0 {
		t.Errorf("processed %d", got.ProcessedCount)
	}
}

func TestShutdownAbandonsAndRequeues(t *testing.T) {
	d := newDB(t)
	acct := seedAccount(t, d, "a", false, 7200)
	b := newBuilder(func(store.SyncSession) *fakeGmail { return &fakeGmail{n: 400, delay: 5 * time.Millisecond} })
	stop := startRunner(t, d, b, nil)
	s := enqueue(t, d, acct, "initial")
	waitFor(t, "running", 10*time.Second, func() bool { return session(t, d, s.ID).ProcessedCount > 0 })
	stop() // SIGTERM equivalent

	if got := session(t, d, s.ID); got.Status != "abandoned" || got.LastError == "" {
		t.Fatalf("session = %+v", got)
	}
	rows, _ := d.ListSessions(context.Background(), store.ListSessionsParams{AccountID: uuid.NullUUID{UUID: acct, Valid: true}, Lim: 10})
	queued := 0
	for _, r := range rows {
		if r.Status == "queued" && r.TriggerType == "recovery" {
			queued++
		}
	}
	if queued != 1 {
		t.Errorf("recovery sessions queued = %d", queued)
	}
}

func TestBuildFailureFailsSessionAndMarksAccount(t *testing.T) {
	d := newDB(t)
	acct := seedAccount(t, d, "a", false, 7200)
	b := newBuilder(func(store.SyncSession) *fakeGmail { return &fakeGmail{} })
	b.err = fmt.Errorf("refresh: %w", clients.ErrNotAuthorised)
	startRunner(t, d, b, nil)
	s := enqueue(t, d, acct, "initial")
	waitFor(t, "failed", 10*time.Second, func() bool { return session(t, d, s.ID).Status == "failed" })
	a, _ := d.GetAccount(context.Background(), acct)
	if a.Status != "needs_reauth" || a.LastConnectionError == "" {
		t.Errorf("account = %+v", a)
	}
}

func TestAuthFailureNotifiesTheWebhookOnceWithoutLeakingSecrets(t *testing.T) {
	d := newDB(t)
	ring, _ := crypto.ParseRing(mustSpec(t))
	factory := &clients.Factory{DB: d, Ring: ring, BaseURL: "https://x"}

	var mu sync.Mutex
	var bodies []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
	}))
	defer hook.Close()
	enc, err := factory.SealWebhook(hook.URL + "/secret-token")
	if err != nil {
		t.Fatal(err)
	}
	cur, _ := d.GetAppSettings(context.Background())
	if _, err := d.UpdateAppSettings(context.Background(), store.UpdateAppSettingsParams{OauthClientID: cur.OauthClientID,
		EncOauthClientSecret: cur.EncOauthClientSecret, SessionRetentionDays: cur.SessionRetentionDays,
		EventRetentionDays: cur.EventRetentionDays, EncNotificationWebhook: enc}); err != nil {
		t.Fatal(err)
	}

	acct := seedAccount(t, d, "a", false, 7200)
	b := newBuilder(func(store.SyncSession) *fakeGmail {
		return &fakeGmail{n: 3, err: fmt.Errorf("get: %w: invalid_grant", gmail.ErrAuth)}
	})
	startRunner(t, d, b, func(o *jobs.Options) {
		o.Notifier = &jobs.Notifier{DB: d, Clients: factory, Logger: logger}
	})
	s := enqueue(t, d, acct, "initial")
	waitFor(t, "failed", 10*time.Second, func() bool { return session(t, d, s.ID).Status == "failed" })
	waitFor(t, "webhook call", 5*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(bodies) > 0 })

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("webhook calls = %d", len(bodies))
	}
	var p map[string]string
	if err := json.Unmarshal([]byte(bodies[0]), &p); err != nil || p["event"] != "needs_reauth" || p["account"] != "a" || p["text"] == "" {
		t.Errorf("payload = %s (%v)", bodies[0], err)
	}
	a, _ := d.GetAccount(context.Background(), acct)
	if a.Status != "needs_reauth" {
		t.Errorf("account status = %s", a.Status)
	}
	_ = pgtype.Timestamptz{}
}

func mustSpec(t *testing.T) string {
	s, err := crypto.GenerateRingSpec("k1")
	if err != nil {
		t.Fatal(err)
	}
	return s
}
