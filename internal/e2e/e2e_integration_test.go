//go:build integration

// Package e2e runs the whole pipeline: a fake Gmail API over HTTP (real
// gmail client with real OAuth token refresh), the real engine, runner and
// PostgreSQL, and a real Dovecot server.
package e2e_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/clients"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/crypto"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/imapdest"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/jobs"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/testenv"
)

// gmailAPI is a tiny fake of the Gmail REST API and Google's token endpoint.
type gmailAPI struct {
	mu       sync.Mutex
	msgs     map[string]*msg
	order    []string
	history  []histRec
	rec      int
	tokens   int
	getCalls map[string]int
}
type msg struct {
	labels []string
	raw    string
	ms     int64
}
type histRec struct {
	id     int
	added  string
	labels []string // labelsAdded
	msgID  string
}

func newGmailAPI() *gmailAPI {
	return &gmailAPI{msgs: map[string]*msg{}, rec: 1000, getCalls: map[string]int{}}
}

func (g *gmailAPI) add(id string, ms int64, labels ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	body := "From: Alice <alice@example.com>\r\nTo: me@gmail.com\r\nSubject: =?utf-8?q?Caf=C3=A9_" + id + "?=\r\nMessage-ID: <" + id + "@ex>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHello " + id + " éè\r\n"
	g.msgs[id] = &msg{labels: labels, raw: body, ms: ms}
	g.order = append(g.order, id)
	g.rec++
	g.history = append(g.history, histRec{id: g.rec, added: id, msgID: id})
}

func (g *gmailAPI) addLabel(id, label string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.msgs[id].labels = append(g.msgs[id].labels, label)
	g.rec++
	g.history = append(g.history, histRec{id: g.rec, msgID: id, labels: []string{label}})
}

func (g *gmailAPI) handler() http.Handler {
	mux := http.NewServeMux()
	j := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.tokens++
		g.mu.Unlock()
		_ = r.ParseForm()
		if r.Form.Get("refresh_token") != "refresh-1" {
			w.WriteHeader(400)
			j(w, map[string]string{"error": "invalid_grant"})
			return
		}
		j(w, map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 3600})
	})
	mux.HandleFunc("/gmail/v1/users/me/profile", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		j(w, map[string]any{"emailAddress": "me@gmail.com", "historyId": strconv.Itoa(g.rec)})
	})
	mux.HandleFunc("/gmail/v1/users/me/labels", func(w http.ResponseWriter, r *http.Request) {
		j(w, map[string]any{"labels": []map[string]string{
			{"id": "INBOX", "name": "INBOX", "type": "system"}, {"id": "SENT", "name": "SENT", "type": "system"},
			{"id": "UNREAD", "name": "UNREAD", "type": "system"}, {"id": "SPAM", "name": "SPAM", "type": "system"},
			{"id": "Label_1", "name": "Projects", "type": "user"}, {"id": "Label_2", "name": "Work/Customers", "type": "user"},
		}})
	})
	mux.HandleFunc("/gmail/v1/users/me/messages", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		start, _ := strconv.Atoi(r.URL.Query().Get("pageToken"))
		size := 4
		var refs []map[string]string
		end := start + size
		if end > len(g.order) {
			end = len(g.order)
		}
		for _, id := range g.order[start:end] {
			refs = append(refs, map[string]string{"id": id, "threadId": "t" + id})
		}
		out := map[string]any{"messages": refs}
		if end < len(g.order) {
			out["nextPageToken"] = strconv.Itoa(end)
		}
		j(w, out)
	})
	mux.HandleFunc("/gmail/v1/users/me/messages/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/gmail/v1/users/me/messages/")
		g.mu.Lock()
		defer g.mu.Unlock()
		g.getCalls[id]++
		m, ok := g.msgs[id]
		if !ok {
			w.WriteHeader(404)
			j(w, map[string]any{"error": map[string]any{"code": 404, "message": "not found"}})
			return
		}
		j(w, map[string]any{"id": id, "threadId": "t" + id, "labelIds": m.labels, "internalDate": strconv.FormatInt(m.ms, 10),
			"historyId": "1", "raw": base64.URLEncoding.EncodeToString([]byte(m.raw))})
	})
	mux.HandleFunc("/gmail/v1/users/me/history", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		start, _ := strconv.Atoi(r.URL.Query().Get("startHistoryId"))
		var hist []map[string]any
		for _, h := range g.history {
			if h.id <= start {
				continue
			}
			rec := map[string]any{"id": strconv.Itoa(h.id)}
			if h.added != "" {
				rec["messagesAdded"] = []map[string]any{{"message": map[string]any{"id": h.msgID, "threadId": "t" + h.msgID}}}
			} else {
				rec["labelsAdded"] = []map[string]any{{"message": map[string]any{"id": h.msgID}, "labelIds": h.labels}}
			}
			hist = append(hist, rec)
		}
		j(w, map[string]any{"history": hist, "historyId": strconv.Itoa(g.rec)})
	})
	return mux
}

// rewrite sends every request, whatever its host, to the local fake.
type rewrite struct{ base *url.URL }

func (rw rewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	if strings.Contains(r.URL.Host, "oauth2") || strings.HasSuffix(r.URL.Path, "/token") {
		r2.URL.Path = "/token"
	}
	r2.URL.Scheme, r2.URL.Host = rw.base.Scheme, rw.base.Host
	return http.DefaultTransport.RoundTrip(r2)
}

func TestWholePipelineAgainstFakeGmailAndRealDovecot(t *testing.T) {
	d := testenv.DB(t)
	imapCfg := testenv.Dovecot(t)
	ctx := context.Background()

	g := newGmailAPI()
	api := httptest.NewServer(g.handler())
	defer api.Close()
	base, _ := url.Parse(api.URL)

	spec, _ := crypto.GenerateRingSpec("k1")
	ring, _ := crypto.ParseRing(spec)
	factory := &clients.Factory{DB: d, Ring: ring, BaseURL: "https://sync.example",
		HTTPClient: &http.Client{Transport: rewrite{base}}, GmailEndpoint: api.URL + "/"}

	// Configuration as the UI would store it, secrets encrypted.
	sec, _ := factory.SealOAuthSecret("client-secret")
	cur, _ := d.GetAppSettings(ctx)
	if _, err := d.UpdateAppSettings(ctx, store.UpdateAppSettingsParams{OauthClientID: "client-id", EncOauthClientSecret: sec,
		SessionRetentionDays: cur.SessionRetentionDays, EventRetentionDays: cur.EventRetentionDays}); err != nil {
		t.Fatal(err)
	}
	destID, acctID := uuid.New(), uuid.New()
	pw, _ := factory.SealPassword(destID, imapCfg.Password)
	if _, err := d.CreateDestination(ctx, store.CreateDestinationParams{ID: destID, Name: "dovecot", Host: imapCfg.Host, Port: int32(imapCfg.Port),
		TlsMode: "none", Username: imapCfg.Username, EncPassword: pw, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateAccount(ctx, store.CreateAccountParams{ID: acctID, Name: "me", DestinationID: uuid.NullUUID{UUID: destID, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	tok, _ := factory.SealRefreshToken(acctID, "refresh-1")
	if _, err := d.SetAccountAuthorised(ctx, store.SetAccountAuthorisedParams{ID: acctID, EncRefreshToken: tok, GmailAddress: "me@gmail.com"}); err != nil {
		t.Fatal(err)
	}
	_ = d.EnsureSyncSettings(ctx, acctID)
	_ = d.EnsureSyncState(ctx, acctID)

	day := func(n int) int64 { return time.Date(2023, 5, n, 10, 0, 0, 0, time.UTC).UnixMilli() }
	g.add("a1", day(1), "INBOX", "UNREAD")
	g.add("a2", day(2), "INBOX", "Label_1")
	g.add("a3", day(3), "Label_2")
	g.add("a4", day(4)) // archived, unlabelled
	g.add("a5", day(5), "SENT")
	g.add("a6", day(6), "SPAM")
	g.add("a7", day(7), "INBOX", "Label_1", "Label_2")
	g.add("a8", day(8), "INBOX")
	g.add("a9", day(9), "INBOX")

	runner := jobs.NewRunner(jobs.Options{DB: d, Builder: &jobs.DBBuilder{DB: d, Clients: factory},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), PollEvery: 20 * time.Millisecond, HeartbeatEvery: 20 * time.Millisecond})
	rctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { runner.Run(rctx); close(done) }()
	defer func() { stop(); <-done }()

	runSession := func(syncType string) store.SyncSession {
		t.Helper()
		s, err := d.CreateSession(ctx, store.CreateSessionParams{ID: uuid.New(), AccountID: acctID, TriggerType: "manual", SyncType: syncType, Params: []byte("{}")})
		if err != nil {
			t.Fatal(err)
		}
		runner.Wake()
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			got, _ := d.GetSession(ctx, s.ID)
			switch got.Status {
			case "completed", "completed_with_errors", "failed", "cancelled", "abandoned":
				return got
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("session did not finish")
		return store.SyncSession{}
	}

	// 1. Initial import (4 per Gmail page, so pagination is exercised).
	s1 := runSession("initial")
	if s1.Status != "completed" || s1.DiscoveredCount != 9 || s1.CopiedCount != 8 || s1.SkippedCount != 1 || s1.FailedCount != 0 {
		t.Fatalf("initial session = %+v", s1)
	}

	dc, err := imapdest.Dial(ctx, imapCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Close()
	idsIn := func(folder string) map[string]bool {
		idx, err := dc.LoadIndex(ctx, folder, 1000)
		if err != nil {
			t.Fatalf("%s: %v", folder, err)
		}
		out := map[string]bool{}
		for v := range idx.IDs {
			out[strings.TrimPrefix(v, acctID.String()+".")] = true
		}
		return out
	}
	want := map[string][]string{
		"INBOX": {"a1", "a2", "a7", "a8", "a9"}, "Projects": {"a2", "a7"}, "Work/Customers": {"a3", "a7"},
		"Archive": {"a4"}, "Sent": {"a5"},
	}
	for folder, ids := range want {
		got := idsIn(folder)
		if len(got) != len(ids) {
			t.Errorf("%s holds %v, want %v", folder, got, ids)
		}
		for _, id := range ids {
			if !got[id] {
				t.Errorf("%s lacks %s", folder, id)
			}
		}
	}
	if names, _ := dc.ListFolders(ctx); contains(names, "Spam") {
		t.Error("spam folder created")
	}

	// 2. A second initial/incremental run delivers nothing new and creates no duplicates.
	s2 := runSession("incremental")
	if s2.Status != "completed" || s2.CopiedCount != 0 {
		t.Fatalf("idle incremental = %+v", s2)
	}

	// 3. New mail and a new label on an old message arrive through the history.
	g.add("b1", day(10), "INBOX", "Label_2")
	g.addLabel("a8", "Label_1")
	s3 := runSession("incremental")
	if s3.Status != "completed" || s3.CopiedCount != 2 {
		t.Fatalf("incremental = %+v", s3)
	}
	if got := idsIn("Work/Customers"); !got["b1"] {
		t.Errorf("Work/Customers = %v", got)
	}
	if got := idsIn("Projects"); !got["a8"] || len(got) != 3 {
		t.Errorf("Projects = %v", got)
	}
	if got := idsIn("INBOX"); len(got) != 6 {
		t.Errorf("INBOX holds %d, want 6 (no duplicates)", len(got))
	}

	// 4. State: checkpoint at the mailbox's current history, one set of rows, no per-message table.
	st, _ := d.GetSyncState(ctx, acctID)
	if !st.InitialSyncCompleted || st.LastSuccessfulHistoryID.Int64 != int64(g.rec) {
		t.Errorf("state = %+v (rec %d)", st, g.rec)
	}
	var tables int
	_ = d.Pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND (table_name ILIKE '%message%' OR table_name ILIKE '%copied%')`).Scan(&tables)
	if tables != 1 { // failed_messages only
		t.Errorf("per-message tables = %d, want only failed_messages", tables)
	}
	var fails int
	_ = d.Pool.QueryRow(ctx, `SELECT count(*) FROM failed_messages`).Scan(&fails)
	if fails != 0 {
		t.Errorf("failures = %d", fails)
	}

	// 5. The real OAuth refresh path ran (byte fidelity is asserted in the imapdest Dovecot test).
	g.mu.Lock()
	tokens := g.tokens
	g.mu.Unlock()
	if tokens == 0 {
		t.Error("the access token was never fetched from the token endpoint")
	}

}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
