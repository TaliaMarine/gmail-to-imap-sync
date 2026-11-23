//go:build integration

package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/api"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/clients"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/config"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/crypto"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/progress"
)

const (
	clientSecret = "sekret-client-secret"
	webhookURL   = "https://hooks.example.com/sekret-webhook"
	imapPassword = "sekret-imap-password"
	refreshToken = "sekret-refresh-token"
)

// harness is the whole application on a real PostgreSQL plus fake Google and
// a fake IMAP server.
type harness struct {
	t       *testing.T
	srv     *httptest.Server
	db      *db.DB
	ring    *crypto.Ring
	google  *fakeGoogle
	imap    cfgIMAP
	bodies  []string // every response body, scanned for leaked secrets at the end
	mu      sync.Mutex
	noJar   *http.Client
	jarHTTP *http.Client
}

type cfgIMAP struct {
	host string
	port int
}

type fakeGoogle struct {
	srv         *httptest.Server
	mu          sync.Mutex
	giveRefresh bool
	tokenCalls  []string
}

// rewrite sends Google's token endpoint to the fake.
type rewrite struct {
	to   *url.URL
	base http.RoundTripper
}

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == "oauth2.googleapis.com" {
		req = req.Clone(req.Context())
		req.URL.Scheme, req.URL.Host = r.to.Scheme, r.to.Host
	}
	return r.base.RoundTrip(req)
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	g := &fakeGoogle{giveRefresh: true}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		g.mu.Lock()
		g.tokenCalls = append(g.tokenCalls, r.PostForm.Get("grant_type"))
		give := g.giveRefresh
		g.mu.Unlock()
		resp := map[string]any{"access_token": "at-1", "token_type": "Bearer", "expires_in": 3600}
		if r.PostForm.Get("grant_type") == "authorization_code" {
			if r.PostForm.Get("code_verifier") == "" {
				http.Error(w, `{"error":"invalid_request"}`, 400)
				return
			}
			if give {
				resp["refresh_token"] = refreshToken
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/gmail/v1/users/me/profile", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-1" {
			http.Error(w, `{"error":{"code":401,"message":"no"}}`, 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"emailAddress":"me@gmail.com","historyId":"42","messagesTotal":7}`))
	})
	mux.HandleFunc("/gmail/v1/users/me/labels", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"labels":[{"id":"INBOX","name":"INBOX","type":"system"},{"id":"Label_1","name":"Work","type":"user"},{"id":"Label_2","name":"work","type":"user"}]}`))
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func startIMAP(t *testing.T) cfgIMAP {
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("archive", imapPassword)
	mem.AddUser(user)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapUIDPlus: {}},
		InsecureAuth: true,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	_ = user.Create("INBOX", nil)
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)
	return cfgIMAP{host, p}
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	pg, err := postgres.Run(ctx, "postgres:17-alpine", postgres.WithDatabase("gas"), postgres.WithUsername("gas"),
		postgres.WithPassword("gas"), postgres.BasicWaitStrategies())
	if err != nil {
		cancel()
		t.Skipf("postgres unavailable: %v", err)
	}
	url, _ := pg.ConnectionString(ctx, "sslmode=disable")
	d, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Migrate(); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := progress.NewHub(d.Pool, logger)
	go hub.Run(ctx)
	t.Cleanup(func() { cancel(); d.Close(); _ = pg.Terminate(context.Background()) })

	spec, _ := crypto.GenerateRingSpec("k1")
	ring, _ := crypto.ParseRing(spec)

	h := &harness{t: t, db: d, ring: ring, google: newFakeGoogle(t), imap: startIMAP(t)}
	h.srv = httptest.NewUnstartedServer(nil)
	base := "http://" + h.srv.Listener.Addr().String()
	gu, _ := http.NewRequest("GET", h.google.srv.URL, nil)
	factory := &clients.Factory{
		DB: d, Ring: ring, BaseURL: base,
		HTTPClient:    &http.Client{Transport: rewrite{to: gu.URL, base: http.DefaultTransport}},
		GmailEndpoint: h.google.srv.URL + "/",
	}
	cfg := &config.Config{BaseURL: base, AuthDisabled: true, LogFormat: "json"}
	a, err := api.New(cfg, api.Deps{DB: d, Logger: logger, Mode: config.ModeAll, StartedAt: time.Now(), Ring: ring, Hub: hub, Clients: factory})
	if err != nil {
		t.Fatal(err)
	}
	h.srv.Config.Handler = a.Router
	h.srv.Start()
	t.Cleanup(h.srv.Close)

	h.noJar = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	jar, _ := cookiejar.New(nil)
	h.jarHTTP = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return h
}

// call performs a JSON request and returns status and decoded body.
func (h *harness) call(method, path string, body any) (int, map[string]any) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.jarHTTP.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	h.mu.Lock()
	h.bodies = append(h.bodies, string(raw))
	h.mu.Unlock()
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func (h *harness) must(wantStatus int, method, path string, body any) map[string]any {
	h.t.Helper()
	st, out := h.call(method, path, body)
	if st != wantStatus {
		h.t.Fatalf("%s %s = %d, want %d: %v", method, path, st, wantStatus, out)
	}
	return out
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func items(m map[string]any) []any          { l, _ := m["items"].([]any); return l }

func (h *harness) imapBody(name string, password string) map[string]any {
	m := map[string]any{"name": name, "host": h.imap.host, "port": h.imap.port, "tls_mode": "none",
		"tls_skip_verify": false, "username": "archive", "enabled": true}
	if password != "" {
		m["password"] = password
	}
	return m
}

func TestAPI(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	var destID, acctID string

	t.Run("settings", func(t *testing.T) {
		s := h.must(200, "GET", "/api/v1/settings", nil)
		if s["configured"] != false || !strings.HasSuffix(str(s, "redirect_uri"), "/oauth/callback") {
			t.Errorf("initial settings = %v", s)
		}
		s = h.must(200, "PUT", "/api/v1/settings", map[string]any{
			"oauth_client_id": "cid.apps.googleusercontent.com", "oauth_client_secret": clientSecret,
			"notification_webhook": webhookURL, "session_retention_days": 365, "event_retention_days": 90,
		})
		if s["configured"] != true || s["oauth_client_secret_set"] != true || s["notification_webhook_set"] != true {
			t.Errorf("settings = %v", s)
		}
		// Omitting the secrets keeps them.
		s = h.must(200, "PUT", "/api/v1/settings", map[string]any{
			"oauth_client_id": "cid.apps.googleusercontent.com", "session_retention_days": 100, "event_retention_days": 30,
		})
		if s["configured"] != true || s["notification_webhook_set"] != true || s["session_retention_days"] != float64(100) {
			t.Errorf("settings after partial update = %v", s)
		}
		if st, _ := h.call("PUT", "/api/v1/settings", map[string]any{"oauth_client_id": "x", "notification_webhook": "ftp://x", "session_retention_days": 1, "event_retention_days": 1}); st != 422 {
			t.Errorf("bad webhook = %d", st)
		}
		if st, _ := h.call("PUT", "/api/v1/settings", map[string]any{"oauth_client_id": "x", "session_retention_days": 0, "event_retention_days": 1}); st != 422 {
			t.Errorf("retention 0 = %d", st)
		}
	})

	t.Run("destinations", func(t *testing.T) {
		if st, _ := h.call("POST", "/api/v1/destinations", h.imapBody("Dovecot", "")); st != 422 {
			t.Errorf("create without password = %d", st)
		}
		bad := h.imapBody("Dovecot", imapPassword)
		bad["tls_mode"] = "ssl"
		if st, _ := h.call("POST", "/api/v1/destinations", bad); st != 422 {
			t.Errorf("bad tls_mode = %d", st)
		}
		bad = h.imapBody("Dovecot", imapPassword)
		bad["port"] = 70000
		if st, _ := h.call("POST", "/api/v1/destinations", bad); st != 422 {
			t.Errorf("bad port = %d", st)
		}
		d := h.must(201, "POST", "/api/v1/destinations", h.imapBody("Dovecot", imapPassword))
		destID = str(d, "id")
		if d["password_set"] != true || d["password"] != nil {
			t.Errorf("destination = %v", d)
		}
		if st, _ := h.call("POST", "/api/v1/destinations", h.imapBody("Dovecot", imapPassword)); st != 409 {
			t.Errorf("duplicate name = %d", st)
		}

		res := h.must(200, "POST", "/api/v1/destinations/"+destID+"/test", nil)
		if res["ok"] != true || res["delimiter"] != "/" {
			t.Errorf("stored test = %v", res)
		}
		got := h.must(200, "GET", "/api/v1/destinations/"+destID, nil)
		if got["hierarchy_delimiter"] != "/" {
			t.Errorf("delimiter not stored: %v", got)
		}
		res = h.must(200, "POST", "/api/v1/destinations/test", h.imapBody("draft", "wrong"))
		if res["ok"] != false || res["error_kind"] != "imap_auth" || strings.Contains(fmt.Sprint(res), "wrong") {
			t.Errorf("bad password test = %v", res)
		}
		draft := h.imapBody("draft", "")
		draft["destination_id"] = destID // reuse the stored password
		if res = h.must(200, "POST", "/api/v1/destinations/test", draft); res["ok"] != true {
			t.Errorf("draft test with stored password = %v", res)
		}

		// The stored password must never follow the connection to another server.
		moved := h.imapBody("draft", "")
		moved["destination_id"] = destID
		moved["host"] = "attacker.example"
		if st, _ := h.call("POST", "/api/v1/destinations/test", moved); st != 422 {
			t.Errorf("draft test with the stored password against another host = %d", st)
		}
		other := h.imapBody("Dovecot", "")
		other["host"] = "attacker.example"
		if st, _ := h.call("PUT", "/api/v1/destinations/"+destID, other); st != 422 {
			t.Errorf("changing the host without the password = %d", st)
		}
		other["password"] = imapPassword
		if st, _ := h.call("PUT", "/api/v1/destinations/"+destID, other); st != 200 {
			t.Errorf("changing the host with the password = %d", st)
		}
		h.must(200, "PUT", "/api/v1/destinations/"+destID, h.imapBody("Dovecot", imapPassword)) // back

		upd := h.imapBody("Dovecot renamed", "") // keeps the password
		d = h.must(200, "PUT", "/api/v1/destinations/"+destID, upd)
		if d["name"] != "Dovecot renamed" || d["password_set"] != true {
			t.Errorf("update = %v", d)
		}
		if res = h.must(200, "POST", "/api/v1/destinations/"+destID+"/test", nil); res["ok"] != true {
			t.Errorf("password lost on update: %v", res)
		}
		if st, _ := h.call("GET", "/api/v1/destinations/"+uuid.NewString(), nil); st != 404 {
			t.Errorf("missing = %d", st)
		}
		if st, _ := h.call("GET", "/api/v1/destinations/not-a-uuid", nil); st != 422 {
			t.Errorf("bad id = %d", st)
		}
	})

	t.Run("accounts and sync settings", func(t *testing.T) {
		if st, _ := h.call("POST", "/api/v1/accounts", map[string]any{"name": "Me", "destination_id": uuid.NewString()}); st != 422 {
			t.Errorf("unknown destination = %d", st)
		}
		a := h.must(201, "POST", "/api/v1/accounts", map[string]any{"name": "Me", "destination_id": destID})
		acctID = str(a, "id")
		if a["status"] != "unauthorised" || a["destination_name"] != "Dovecot renamed" {
			t.Errorf("account = %v", a)
		}
		if st, _ := h.call("POST", "/api/v1/accounts", map[string]any{"name": "Me"}); st != 409 {
			t.Errorf("duplicate = %d", st)
		}
		if l := items(h.must(200, "GET", "/api/v1/accounts", nil)); len(l) != 1 {
			t.Errorf("accounts = %v", l)
		}
		if st, _ := h.call("DELETE", "/api/v1/destinations/"+destID, nil); st != 409 {
			t.Errorf("deleting a destination in use = %d", st)
		}

		ss := h.must(200, "GET", "/api/v1/accounts/"+acctID+"/sync-settings", nil)
		if ss["archive_folder"] != "Archive" || ss["schedule_interval_seconds"] != float64(7200) {
			t.Errorf("default sync settings = %v", ss)
		}
		ss["enabled"] = true
		ss["schedule_interval_seconds"] = 3600
		ss["initial_sync_after"] = "2024-01-31"
		ss["label_mapping"] = map[string]any{"Label_1": "Clients/Work"}
		ss["excluded_labels"] = []any{map[string]any{"kind": "prefix", "value": "Newsletters"}}
		out := h.must(200, "PUT", "/api/v1/accounts/"+acctID+"/sync-settings", ss)
		if out["initial_sync_after"] != "2024-01-31" || out["enabled"] != true {
			t.Errorf("updated = %v", out)
		}
		bad := map[string]any{}
		for k, v := range ss {
			bad[k] = v
		}
		bad["schedule_interval_seconds"] = 60
		if st, _ := h.call("PUT", "/api/v1/accounts/"+acctID+"/sync-settings", bad); st != 422 {
			t.Errorf("interval 60 = %d", st)
		}
		bad["schedule_interval_seconds"] = 3600
		bad["excluded_labels"] = []any{map[string]any{"kind": "regex", "value": "x"}}
		if st, _ := h.call("PUT", "/api/v1/accounts/"+acctID+"/sync-settings", bad); st != 422 {
			t.Errorf("bad matcher = %d", st)
		}
		bad["excluded_labels"] = []any{}
		bad["initial_sync_after"] = "soon"
		if st, _ := h.call("PUT", "/api/v1/accounts/"+acctID+"/sync-settings", bad); st != 422 {
			t.Errorf("bad date = %d", st)
		}
	})

	t.Run("labels and folder preview", func(t *testing.T) {
		id := uuid.MustParse(acctID)
		for _, l := range [][3]string{{"INBOX", "INBOX", "system"}, {"Label_1", "Work", "user"}, {"Label_2", "work", "user"}, {"Label_3", "Newsletters/Weekly", "user"}} {
			if err := h.db.UpsertLabel(ctx, store.UpsertLabelParams{AccountID: id, LabelID: l[0], Name: l[1], Type: l[2]}); err != nil {
				t.Fatal(err)
			}
		}
		byID := func(m map[string]any) map[string]map[string]any {
			out := map[string]map[string]any{}
			for _, it := range items(m) {
				x := it.(map[string]any)
				out[str(x, "id")] = x
			}
			return out
		}
		l := byID(h.must(200, "GET", "/api/v1/accounts/"+acctID+"/labels", nil))
		if l["Label_1"]["folder"] != "Clients/Work" || l["Label_3"]["excluded"] != true || l["INBOX"]["folder"] != "INBOX" {
			t.Errorf("labels = %v", l)
		}
		// Preview of candidate settings that make Label_1 and Label_2 collide.
		cand := h.must(200, "GET", "/api/v1/accounts/"+acctID+"/sync-settings", nil)
		cand["label_mapping"] = map[string]any{}
		p := byID(h.must(200, "POST", "/api/v1/accounts/"+acctID+"/folder-preview", cand))
		if c, _ := p["Label_1"]["collides_with"].([]any); len(c) != 1 || c[0] != "Label_2" {
			t.Errorf("collision not reported: %v", p["Label_1"])
		}
		// Preview did not save anything.
		if l = byID(h.must(200, "GET", "/api/v1/accounts/"+acctID+"/labels", nil)); l["Label_1"]["folder"] != "Clients/Work" {
			t.Error("preview changed stored settings")
		}
		cand["archive_folder"] = " / "
		if st, _ := h.call("POST", "/api/v1/accounts/"+acctID+"/folder-preview", cand); st != 422 {
			t.Errorf("invalid candidate = %d", st)
		}
	})

	t.Run("sync is blocked until authorised", func(t *testing.T) {
		if st, out := h.call("POST", "/api/v1/accounts/"+acctID+"/sync", map[string]any{}); st != 409 {
			t.Errorf("sync before authorisation = %d %v", st, out)
		}
		if st, _ := h.call("POST", "/api/v1/accounts/"+acctID+"/labels/refresh", nil); st != 409 {
			t.Errorf("label refresh before authorisation = %d", st)
		}
	})

	t.Run("google oauth", func(t *testing.T) {
		start := h.must(200, "POST", "/api/v1/accounts/"+acctID+"/oauth/start", nil)
		u, err := url.Parse(str(start, "url"))
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		if q.Get("access_type") != "offline" || q.Get("prompt") != "consent" || q.Get("code_challenge_method") != "S256" ||
			q.Get("code_challenge") == "" || q.Get("scope") != "https://www.googleapis.com/auth/gmail.readonly" || q.Get("state") == "" {
			t.Errorf("auth url = %s", u)
		}
		state := q.Get("state")

		callback := func(client *http.Client, state, code string, extra string) *http.Response {
			resp, err := client.Get(h.srv.URL + "/oauth/callback?state=" + url.QueryEscape(state) + "&code=" + code + extra)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			return resp
		}
		// A different browser (no state cookie) is rejected.
		if loc := callback(h.noJar, state, "c", "").Header.Get("Location"); !strings.Contains(loc, "oauth_error=missing_state") {
			t.Errorf("no cookie: %s", loc)
		}
		// Wrong state.
		if loc := callback(h.jarHTTP, "forged", "c", "").Header.Get("Location"); !strings.Contains(loc, "oauth_error=state_mismatch") || !strings.Contains(loc, acctID) {
			t.Errorf("forged state: %s", loc)
		}
		// The cookie is single use: even the right state now fails.
		if loc := callback(h.jarHTTP, state, "c", "").Header.Get("Location"); !strings.Contains(loc, "oauth_error=missing_state") {
			t.Errorf("replayed state: %s", loc)
		}

		// Google withholds the refresh token.
		h.google.mu.Lock()
		h.google.giveRefresh = false
		h.google.mu.Unlock()
		start = h.must(200, "POST", "/api/v1/accounts/"+acctID+"/oauth/start", nil)
		u, _ = url.Parse(str(start, "url"))
		if loc := callback(h.jarHTTP, u.Query().Get("state"), "c", "").Header.Get("Location"); !strings.Contains(loc, "oauth_error=no_refresh_token") {
			t.Errorf("no refresh token: %s", loc)
		}
		if a := h.must(200, "GET", "/api/v1/accounts/"+acctID, nil); a["status"] != "unauthorised" {
			t.Errorf("account changed by a failed authorisation: %v", a)
		}
		// The user denies consent.
		start = h.must(200, "POST", "/api/v1/accounts/"+acctID+"/oauth/start", nil)
		u, _ = url.Parse(str(start, "url"))
		if loc := callback(h.jarHTTP, u.Query().Get("state"), "", "&error=access_denied").Header.Get("Location"); !strings.Contains(loc, "oauth_error=denied") {
			t.Errorf("denied: %s", loc)
		}

		// Success.
		h.google.mu.Lock()
		h.google.giveRefresh = true
		h.google.mu.Unlock()
		start = h.must(200, "POST", "/api/v1/accounts/"+acctID+"/oauth/start", nil)
		u, _ = url.Parse(str(start, "url"))
		resp := callback(h.jarHTTP, u.Query().Get("state"), "good", "")
		if resp.StatusCode != 302 || resp.Header.Get("Location") != "/accounts/"+acctID {
			t.Fatalf("callback = %d %s", resp.StatusCode, resp.Header.Get("Location"))
		}
		a := h.must(200, "GET", "/api/v1/accounts/"+acctID, nil)
		if a["status"] != "ok" || a["gmail_address"] != "me@gmail.com" {
			t.Errorf("account after authorisation = %v", a)
		}
		// Encrypted at rest, bound to the account row.
		acct, _ := h.db.GetAccount(ctx, uuid.MustParse(acctID))
		if strings.Contains(acct.EncRefreshToken, refreshToken) {
			t.Error("refresh token stored in clear")
		}
		pt, err := h.ring.Decrypt(acct.EncRefreshToken, "accounts.refresh_token."+acctID)
		if err != nil || string(pt) != refreshToken {
			t.Errorf("stored token does not decrypt: %v", err)
		}
	})

	t.Run("account test and label refresh use the stored token", func(t *testing.T) {
		res := h.must(200, "POST", "/api/v1/accounts/"+acctID+"/test", nil)
		g, im := res["gmail"].(map[string]any), res["imap"].(map[string]any)
		if g["ok"] != true || g["email"] != "me@gmail.com" || im["ok"] != true {
			t.Errorf("test = %v", res)
		}
		l := h.must(200, "POST", "/api/v1/accounts/"+acctID+"/labels/refresh", nil)
		names := map[string]bool{}
		for _, it := range items(l) {
			names[str(it.(map[string]any), "id")] = true
		}
		// Label_3 is gone from Gmail, so it leaves the cache.
		if !names["Label_1"] || !names["Label_2"] || !names["INBOX"] || names["Label_3"] {
			t.Errorf("labels after refresh = %v", names)
		}
	})

	t.Run("sync enqueue, idempotency and cancel", func(t *testing.T) {
		first := h.must(200, "POST", "/api/v1/accounts/"+acctID+"/sync", map[string]any{})
		s1 := first["session"].(map[string]any)
		if s1["status"] != "queued" || s1["sync_type"] != "initial" || s1["trigger_type"] != "initial" || first["already_active"] != false {
			t.Fatalf("first = %v", first)
		}
		second := h.must(200, "POST", "/api/v1/accounts/"+acctID+"/sync", map[string]any{"type": "reconcile"})
		if second["already_active"] != true || second["session"].(map[string]any)["id"] != s1["id"] {
			t.Errorf("second = %v", second)
		}
		cur := h.must(200, "GET", "/api/v1/accounts/"+acctID+"/sync/current", nil)
		if cur["active"] != true || cur["session"].(map[string]any)["id"] != s1["id"] {
			t.Errorf("current = %v", cur)
		}
		c := h.must(200, "POST", "/api/v1/accounts/"+acctID+"/sync/cancel", nil)
		if c["status"] != "cancelled" {
			t.Errorf("a queued session is cancelled at once: %v", c)
		}
		if st, _ := h.call("POST", "/api/v1/accounts/"+acctID+"/sync/cancel", nil); st != 404 {
			t.Errorf("cancel with nothing active = %d", st)
		}
		cur = h.must(200, "GET", "/api/v1/accounts/"+acctID+"/sync/current", nil)
		if cur["active"] != false || cur["session"].(map[string]any)["status"] != "cancelled" {
			t.Errorf("current after cancel = %v", cur)
		}

		// Once the first sync has completed, requests are honoured as asked and parameters kept.
		if _, err := h.db.Pool.Exec(ctx, `UPDATE sync_state SET initial_sync_completed = true WHERE account_id = $1`, acctID); err != nil {
			t.Fatal(err)
		}
		r := h.must(200, "POST", "/api/v1/accounts/"+acctID+"/sync", map[string]any{"type": "reconcile", "after": "2024-02-01", "label_id": "Label_1"})
		sess := r["session"].(map[string]any)
		if sess["sync_type"] != "reconcile" || sess["trigger_type"] != "manual" {
			t.Errorf("reconcile = %v", sess)
		}
		var params string
		_ = h.db.Pool.QueryRow(ctx, `SELECT params::text FROM sync_sessions WHERE id = $1`, sess["id"]).Scan(&params)
		if !strings.Contains(params, `"2024-02-01"`) || !strings.Contains(params, "Label_1") {
			t.Errorf("params = %s", params)
		}
		if st, _ := h.call("POST", "/api/v1/accounts/"+acctID+"/sync", map[string]any{"type": "reconcile", "after": "soon"}); st != 422 && st != 200 {
			t.Errorf("bad after = %d", st)
		}

		// A running session is cancelled gracefully: flagged, not ended.
		if _, err := h.db.ClaimSession(ctx, store.ClaimSessionParams{WorkerID: "w1", LeaseSecs: 60}); err != nil {
			t.Fatal(err)
		}
		c = h.must(200, "POST", "/api/v1/accounts/"+acctID+"/sync/cancel", nil)
		if c["status"] != "running" || c["cancel_requested"] != true {
			t.Errorf("running cancel = %v", c)
		}
		if st, _ := h.call("DELETE", "/api/v1/accounts/"+acctID, nil); st != 409 {
			t.Errorf("deleting an account with a running sync = %d", st)
		}
		// The worker ends it.
		if _, err := h.db.Pool.Exec(ctx, `UPDATE sync_sessions SET status = 'cancelled', finished_at = now() WHERE account_id = $1 AND status = 'running'`, acctID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("failures", func(t *testing.T) {
		aid := uuid.MustParse(acctID)
		var ids []string
		for i := 0; i < 5; i++ {
			f, err := h.db.UpsertFailure(ctx, store.UpsertFailureParams{
				ID: uuid.New(), AccountID: aid, GmailMessageID: fmt.Sprintf("m%d", i), Subject: fmt.Sprintf("Invoice %d", i),
				Sender: "billing@example.com", Recipients: []byte(`["me@gmail.com"]`),
				IntendedFolders: []byte(`["INBOX","Work"]`), CompletedFolders: []byte(`["INBOX"]`), FailedFolders: []byte(`["Work"]`),
				ErrorCategory: "imap_append", ErrorMessage: "mailbox full", ErrorHistory: []byte(`[{"category":"imap_append","message":"mailbox full"}]`),
			})
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, f.ID.String())
			time.Sleep(5 * time.Millisecond) // distinct last_failed_at for stable paging
		}
		// Pagination, newest first, no overlap.
		seen := map[string]bool{}
		cursor := ""
		pages := 0
		for {
			path := "/api/v1/failures?limit=2"
			if cursor != "" {
				path += "&cursor=" + url.QueryEscape(cursor)
			}
			page := h.must(200, "GET", path, nil)
			for _, it := range items(page) {
				id := str(it.(map[string]any), "id")
				if seen[id] {
					t.Errorf("failure %s on two pages", id)
				}
				seen[id] = true
			}
			pages++
			if cursor = str(page, "next_cursor"); cursor == "" {
				break
			}
		}
		if len(seen) != 5 || pages != 3 {
			t.Errorf("paging saw %d failures in %d pages", len(seen), pages)
		}
		if l := items(h.must(200, "GET", "/api/v1/failures?search=invoice+3", nil)); len(l) != 1 {
			t.Errorf("search = %v", l)
		}
		if l := items(h.must(200, "GET", "/api/v1/failures?category=gmail_auth", nil)); len(l) != 0 {
			t.Errorf("category filter = %v", l)
		}
		d := h.must(200, "GET", "/api/v1/failures/"+ids[0], nil)
		if d["category"] != "imap_append" || d["auto_retry"] != true || str(d, "description") == "" ||
			len(d["error_history"].([]any)) != 1 || len(d["recipients"].([]any)) != 1 || d["failed_folders"].([]any)[0] != "Work" {
			t.Errorf("detail = %v", d)
		}

		ig := h.must(200, "POST", "/api/v1/failures/"+ids[0]+"/ignore", nil)
		if ig["status"] != "ignored" {
			t.Errorf("ignore = %v", ig)
		}
		if l := items(h.must(200, "GET", "/api/v1/failures?status=ignored", nil)); len(l) != 1 {
			t.Errorf("ignored list = %v", l)
		}
		if un := h.must(200, "POST", "/api/v1/failures/"+ids[0]+"/unignore", nil); un["status"] != "pending" {
			t.Errorf("unignore = %v", un)
		}

		// Retry one: failure reset and a retry session queued with its id.
		r := h.must(200, "POST", "/api/v1/failures/"+ids[1]+"/retry", nil)
		ss := r["sessions"].([]any)
		if r["reset"] != float64(1) || len(ss) != 1 || ss[0].(map[string]any)["queued"] != true {
			t.Fatalf("retry one = %v", r)
		}
		sid := str(ss[0].(map[string]any), "session_id")
		var typ, params string
		_ = h.db.Pool.QueryRow(ctx, `SELECT sync_type, params::text FROM sync_sessions WHERE id = $1`, sid).Scan(&typ, &params)
		if typ != "retry" || !strings.Contains(params, ids[1]) {
			t.Errorf("retry session = %s %s", typ, params)
		}
		// With a session already active, more retries are reset but not queued behind it.
		r = h.must(200, "POST", "/api/v1/failures/retry", map[string]any{"all": true})
		ss = r["sessions"].([]any)
		if r["reset"] != float64(5) || len(ss) != 1 || ss[0].(map[string]any)["queued"] != false || str(ss[0].(map[string]any), "reason") == "" {
			t.Errorf("retry all while active = %v", r)
		}
		if st, _ := h.call("POST", "/api/v1/failures/retry", map[string]any{}); st != 422 {
			t.Errorf("retry nothing = %d", st)
		}
		if st, _ := h.call("POST", "/api/v1/failures/retry", map[string]any{"ids": []string{"nope"}}); st != 422 {
			t.Errorf("bad ids = %d", st)
		}
		if st, _ := h.call("GET", "/api/v1/failures/"+uuid.NewString(), nil); st != 404 {
			t.Errorf("missing failure = %d", st)
		}
		h.must(200, "POST", "/api/v1/failures/"+ids[2]+"/ignore", nil)
		if c := h.must(200, "POST", "/api/v1/failures/clear-ignored", map[string]any{}); c["cleared"] != float64(1) {
			t.Errorf("clear ignored = %v", c)
		}
		h.must(200, "POST", "/api/v1/accounts/"+acctID+"/sync/cancel", nil) // leave nothing active
	})

	t.Run("sessions pagination and events", func(t *testing.T) {
		aid := uuid.MustParse(acctID)
		for i := 0; i < 5; i++ {
			s, err := h.db.CreateSession(ctx, store.CreateSessionParams{ID: uuid.New(), AccountID: aid, TriggerType: "scheduled", SyncType: "incremental", Params: []byte("{}")})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.db.Pool.Exec(ctx, `UPDATE sync_sessions SET status = 'completed', copied_count = 10, finished_at = now(), started_at = now() WHERE id = $1`, s.ID); err != nil {
				t.Fatal(err)
			}
			if i == 0 {
				if err := h.db.InsertEvent(ctx, store.InsertEventParams{SessionID: s.ID, EventType: "copied", Severity: "info", Message: "Copied 10 messages", Metadata: []byte(`{"folder":"INBOX"}`)}); err != nil {
					t.Fatal(err)
				}
				ev := h.must(200, "GET", "/api/v1/sessions/"+s.ID.String()+"/events", nil)
				if l := items(ev); len(l) != 1 || l[0].(map[string]any)["metadata"].(map[string]any)["folder"] != "INBOX" {
					t.Errorf("events = %v", ev)
				}
				if l := items(h.must(200, "GET", fmt.Sprintf("/api/v1/sessions/%s/events?after=%d", s.ID, int64(ev["items"].([]any)[0].(map[string]any)["id"].(float64))), nil)); len(l) != 0 {
					t.Errorf("events after = %v", l)
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
		seen, cursor, pages := map[string]bool{}, "", 0
		for {
			path := "/api/v1/sessions?limit=3&status=completed&account=" + acctID
			if cursor != "" {
				path += "&cursor=" + url.QueryEscape(cursor)
			}
			page := h.must(200, "GET", path, nil)
			for _, it := range items(page) {
				id := str(it.(map[string]any), "id")
				if seen[id] {
					t.Errorf("session %s twice", id)
				}
				seen[id] = true
			}
			pages++
			if cursor = str(page, "next_cursor"); cursor == "" {
				break
			}
		}
		if len(seen) != 5 || pages != 2 {
			t.Errorf("saw %d sessions in %d pages", len(seen), pages)
		}
		if st, _ := h.call("GET", "/api/v1/sessions?cursor=garbage", nil); st != 422 {
			t.Errorf("bad cursor = %d", st)
		}
		if st, _ := h.call("GET", "/api/v1/sessions?status=bogus", nil); st != 422 {
			t.Errorf("bad status = %d", st)
		}
		if st, _ := h.call("GET", "/api/v1/sessions/"+uuid.NewString(), nil); st != 404 {
			t.Errorf("missing session = %d", st)
		}
	})

	t.Run("overview and status", func(t *testing.T) {
		o := h.must(200, "GET", "/api/v1/overview", nil)
		accts := o["accounts"].([]any)
		if len(accts) != 1 || len(o["activity"].([]any)) != 14 || len(o["recent_sessions"].([]any)) != 8 {
			t.Fatalf("overview = %v", o)
		}
		a := accts[0].(map[string]any)
		if a["failures"].(map[string]any)["pending"] != float64(4) || a["next_scheduled_sync"] == nil || a["last_session"] == nil {
			t.Errorf("account overview = %v", a)
		}
		var copied float64
		for _, d := range o["activity"].([]any) {
			copied += d.(map[string]any)["copied"].(float64)
		}
		if copied != 50 || o["pending_failures"] != float64(4) {
			t.Errorf("activity copied %v pending %v", copied, o["pending_failures"])
		}
		s := h.must(200, "GET", "/api/v1/system/status", nil)
		if s["database"].(map[string]any)["ok"] != true || s["mode"] != "all" || s["oauth_needs_reauth"] != float64(0) || s["last_successful_session"] == nil {
			t.Errorf("status = %v", s)
		}
	})

	t.Run("event stream", func(t *testing.T) {
		// An active session exists when the client connects...
		aid := uuid.MustParse(acctID)
		active, err := h.db.CreateSession(ctx, store.CreateSessionParams{ID: uuid.New(), AccountID: aid, TriggerType: "manual", SyncType: "incremental", Params: []byte("{}")})
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest("GET", h.srv.URL+"/api/v1/events?account="+acctID, nil)
		req.Header.Set("Last-Event-ID", "7") // a reconnect: still just a fresh snapshot
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		resp, err := http.DefaultClient.Do(req.WithContext(cctx))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
			t.Fatalf("content type %q", ct)
		}
		sc := bufio.NewScanner(resp.Body)
		next := func() (event string, data string) {
			for sc.Scan() {
				line := sc.Text()
				switch {
				case strings.HasPrefix(line, "event: "):
					event = strings.TrimPrefix(line, "event: ")
				case strings.HasPrefix(line, "data: "):
					data = strings.TrimPrefix(line, "data: ")
				case line == "" && event != "":
					return
				}
			}
			t.Fatal("stream ended")
			return
		}
		ev, data := next()
		if ev != "snapshot" || !strings.Contains(data, active.ID.String()) {
			t.Fatalf("first event %s %s", ev, data)
		}
		// ...and a change arrives as a "session" event.
		if _, err := h.db.Pool.Exec(ctx, `UPDATE sync_sessions SET copied_count = 99 WHERE id = $1`, active.ID); err != nil {
			t.Fatal(err)
		}
		if err := progress.Notify(ctx, h.db.Pool, progress.Event{Kind: progress.KindProgress, AccountID: acctID, SessionID: active.ID.String()}); err != nil {
			t.Fatal(err)
		}
		ev, data = next()
		var s map[string]any
		_ = json.Unmarshal([]byte(data), &s)
		if ev != "session" || s["copied"] != float64(99) {
			t.Errorf("session event %s %s", ev, data)
		}
		// Another account's events are not delivered; a bad filter is rejected.
		if st, _ := h.call("GET", "/api/v1/events?account=nope", nil); st != 422 {
			t.Errorf("bad account filter = %d", st)
		}
		cancel()
		_ = h.db.Pool.QueryRow(ctx, `SELECT 1`).Scan(new(int))
		if _, err := h.db.Pool.Exec(ctx, `UPDATE sync_sessions SET status = 'cancelled', finished_at = now() WHERE id = $1`, active.ID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("deleting an account cascades", func(t *testing.T) {
		h.must(204, "DELETE", "/api/v1/accounts/"+acctID, nil)
		var n int
		_ = h.db.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM sync_sessions)+(SELECT count(*) FROM failed_messages)+(SELECT count(*) FROM account_sync_settings)`).Scan(&n)
		if n != 0 {
			t.Errorf("%d rows left behind", n)
		}
		h.must(204, "DELETE", "/api/v1/destinations/"+destID, nil)
	})

	t.Run("no secret ever leaves the API", func(t *testing.T) {
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, b := range h.bodies {
			for _, secret := range []string{clientSecret, webhookURL, imapPassword, refreshToken, "enc_", "at-1"} {
				if strings.Contains(b, secret) {
					t.Fatalf("response leaks %q: %.300s", secret, b)
				}
			}
		}
	})
}
