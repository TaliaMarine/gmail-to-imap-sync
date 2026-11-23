package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// fakeGmail is a minimal Gmail API for tests. Each handler may be replaced.
type fakeGmail struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	hits     map[string]int // by route, e.g. "messages.get"
	handlers map[string]func(w http.ResponseWriter, r *http.Request, hit int)
	auth     []string
}

func newFake(t *testing.T) (*fakeGmail, *Client, *fakeClock) {
	t.Helper()
	f := &fakeGmail{t: t, hits: map[string]int{}, handlers: map[string]func(http.ResponseWriter, *http.Request, int){}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)

	c, err := NewClient(context.Background(), Options{
		HTTPClient: f.srv.Client(), Endpoint: f.srv.URL + "/",
		BaseBackoff: time.Second, MaxBackoff: 30 * time.Second, MaxAttempts: 4, RateLimitAttempts: 4,
		MaxMessageBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	fc := newFakeClock()
	c.sleep = fc.sleep
	c.budget.now, c.budget.sleep, c.budget.last = fc.now, fc.sleep, fc.now()
	return f, c, fc
}

func routeOf(r *http.Request) string {
	p := strings.TrimPrefix(r.URL.Path, "/gmail/v1/users/me/")
	switch {
	case p == "profile":
		return "profile"
	case p == "labels":
		return "labels.list"
	case p == "messages":
		return "messages.list"
	case strings.HasPrefix(p, "messages/"):
		return "messages.get"
	case p == "history":
		return "history.list"
	}
	return p
}

func (f *fakeGmail) serve(w http.ResponseWriter, r *http.Request) {
	route := routeOf(r)
	f.mu.Lock()
	f.hits[route]++
	hit := f.hits[route]
	h := f.handlers[route]
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	if h == nil {
		http.Error(w, "no handler for "+route, http.StatusInternalServerError)
		return
	}
	h(w, r, hit)
}

func (f *fakeGmail) on(route string, h func(w http.ResponseWriter, r *http.Request, hit int)) {
	f.mu.Lock()
	f.handlers[route] = h
	f.mu.Unlock()
}

func (f *fakeGmail) count(route string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[route]
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, code int, reason string, hdr map[string]string) {
	for k, v := range hdr {
		w.Header().Set(k, v)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"code": code, "message": reason,
		"errors": []map[string]string{{"reason": reason, "message": reason}},
	}})
}

func TestProfileAndLabels(t *testing.T) {
	f, c, _ := newFake(t)
	f.on("profile", func(w http.ResponseWriter, r *http.Request, _ int) {
		writeJSON(w, map[string]any{"emailAddress": "me@gmail.com", "historyId": "123456789012", "messagesTotal": 42})
	})
	f.on("labels.list", func(w http.ResponseWriter, r *http.Request, _ int) {
		writeJSON(w, map[string]any{"labels": []map[string]string{
			{"id": "INBOX", "name": "INBOX", "type": "system"},
			{"id": "Label_1", "name": "Work/Customers", "type": "user"},
		}})
	})
	p, err := c.Profile(context.Background())
	if err != nil || p.Email != "me@gmail.com" || p.HistoryID != 123456789012 || p.Messages != 42 {
		t.Fatalf("profile = %+v, %v", p, err)
	}
	ls, err := c.Labels(context.Background())
	if err != nil || len(ls) != 2 || !ls[0].System || ls[1].System || ls[1].Name != "Work/Customers" {
		t.Fatalf("labels = %+v, %v", ls, err)
	}
	if c.Requests() != 2 {
		t.Errorf("requests = %d", c.Requests())
	}
}

func TestListMessagesPaginationAndParams(t *testing.T) {
	f, c, _ := newFake(t)
	var queries []string
	f.on("messages.list", func(w http.ResponseWriter, r *http.Request, hit int) {
		queries = append(queries, r.URL.RawQuery)
		switch r.URL.Query().Get("pageToken") {
		case "":
			writeJSON(w, map[string]any{"messages": []map[string]string{{"id": "a", "threadId": "t1"}, {"id": "b", "threadId": "t1"}},
				"nextPageToken": "p2", "resultSizeEstimate": 3})
		case "p2":
			writeJSON(w, map[string]any{"messages": []map[string]string{{"id": "c", "threadId": "t2"}}})
		default:
			t.Errorf("unexpected page token %q", r.URL.Query().Get("pageToken"))
		}
	})
	var ids []string
	token := ""
	for {
		page, err := c.ListMessages(context.Background(), ListOptions{Query: "after:2024/01/01", LabelIDs: []string{"Label_1"}, PageToken: token, PageSize: 9999})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range page.Messages {
			ids = append(ids, m.ID)
		}
		if token = page.NextPageToken; token == "" {
			break
		}
	}
	if strings.Join(ids, ",") != "a,b,c" {
		t.Errorf("ids = %v", ids)
	}
	q := queries[0]
	for _, want := range []string{"maxResults=500", "labelIds=Label_1", "q=after%3A2024%2F01%2F01"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q lacks %q", q, want)
		}
	}
	if strings.Contains(q, "includeSpamTrash=true") {
		t.Error("spam and trash must stay excluded")
	}
}

func rawMessage(id string, raw string, labels ...string) map[string]any {
	return map[string]any{
		"id": id, "threadId": "t-" + id, "labelIds": labels, "historyId": "777",
		"internalDate": "1700000000123", "sizeEstimate": len(raw),
		"raw": base64.URLEncoding.EncodeToString([]byte(raw)), // padded, as Gmail may send
	}
}

func TestGetRawPreservesBytesAndMetadata(t *testing.T) {
	f, c, _ := newFake(t)
	// CRLF, 8-bit content and trailing newline must survive untouched.
	body := "From: a@x\r\nSubject: =?utf-8?q?caf=C3=A9?=\r\n\r\nH\xe9llo\r\n\r\n"
	f.on("messages.get", func(w http.ResponseWriter, r *http.Request, _ int) {
		if got := r.URL.Query().Get("format"); got != "raw" {
			t.Errorf("format = %q", got)
		}
		writeJSON(w, rawMessage("m1", body, "INBOX", "UNREAD"))
	})
	m, err := c.GetRaw(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	if string(m.Raw) != body {
		t.Errorf("raw bytes changed: %q", m.Raw)
	}
	if m.ID != "m1" || m.ThreadID != "t-m1" || strings.Join(m.LabelIDs, ",") != "INBOX,UNREAD" || m.HistoryID != 777 {
		t.Errorf("metadata = %+v", m)
	}
	if want := time.UnixMilli(1700000000123).UTC(); !m.InternalDate.Equal(want) {
		t.Errorf("internal date = %v, want %v", m.InternalDate, want)
	}
}

func TestGetRawNotFoundAndTooLarge(t *testing.T) {
	f, c, fc := newFake(t)
	f.on("messages.get", func(w http.ResponseWriter, r *http.Request, _ int) {
		if strings.HasSuffix(r.URL.Path, "/gone") {
			apiError(w, 404, "notFound", nil)
			return
		}
		writeJSON(w, rawMessage("big", strings.Repeat("x", 2<<20)))
	})
	_, err := c.GetRaw(context.Background(), "gone")
	if !errors.Is(err, ErrMessageNotFound) || Classify(err) != KindUnavailable {
		t.Errorf("err = %v (kind %v)", err, Classify(err))
	}
	if f.count("messages.get") != 1 || len(fc.slept) != 0 {
		t.Errorf("a 404 must not be retried (hits %d, slept %v)", f.count("messages.get"), fc.slept)
	}
	_, err = c.GetRaw(context.Background(), "big")
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Errorf("err = %v", err)
	}
}

func TestHistoryPaginationAndCheckpoint(t *testing.T) {
	f, c, _ := newFake(t)
	f.on("history.list", func(w http.ResponseWriter, r *http.Request, _ int) {
		q := r.URL.Query()
		if q.Get("startHistoryId") != "100" {
			t.Errorf("startHistoryId = %q", q.Get("startHistoryId"))
		}
		if got := q["historyTypes"]; len(got) != 2 {
			t.Errorf("historyTypes = %v", got)
		}
		switch q.Get("pageToken") {
		case "":
			writeJSON(w, map[string]any{"historyId": "150", "nextPageToken": "n2", "history": []map[string]any{
				{"id": "101", "messagesAdded": []map[string]any{{"message": map[string]any{"id": "m1", "threadId": "t1"}}}},
				{"id": "102", "labelsAdded": []map[string]any{{"message": map[string]any{"id": "m0"}, "labelIds": []string{"Label_9"}}}},
			}})
		case "n2":
			writeJSON(w, map[string]any{"historyId": "160"}) // nothing new: only the checkpoint moves
		}
	})
	p1, err := c.History(context.Background(), 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.MessagesAdded) != 1 || p1.MessagesAdded[0].ID != "m1" || len(p1.LabelsAdded) != 1 ||
		p1.LabelsAdded[0].MessageID != "m0" || p1.LabelsAdded[0].LabelIDs[0] != "Label_9" ||
		p1.NextPageToken != "n2" || p1.HistoryID != 150 || p1.LastRecordID != 102 {
		t.Errorf("page 1 = %+v", p1)
	}
	p2, err := c.History(context.Background(), 100, "n2")
	if err != nil || p2.NextPageToken != "" || p2.HistoryID != 160 || len(p2.MessagesAdded) != 0 {
		t.Errorf("page 2 = %+v, %v", p2, err)
	}
}

func TestHistoryExpired(t *testing.T) {
	f, c, fc := newFake(t)
	f.on("history.list", func(w http.ResponseWriter, r *http.Request, _ int) { apiError(w, 404, "notFound", nil) })
	_, err := c.History(context.Background(), 5, "")
	if !errors.Is(err, ErrHistoryExpired) {
		t.Fatalf("err = %v", err)
	}
	if f.count("history.list") != 1 || len(fc.slept) != 0 {
		t.Error("expired history must not be retried")
	}
}

func TestRetryAfterIsHonouredThenSucceeds(t *testing.T) {
	f, c, fc := newFake(t)
	fc.advTo = false // keep the rate from recovering while we look at it
	f.on("profile", func(w http.ResponseWriter, r *http.Request, hit int) {
		if hit == 1 {
			apiError(w, 429, "rateLimitExceeded", map[string]string{"Retry-After": "17"})
			return
		}
		writeJSON(w, map[string]any{"emailAddress": "me@gmail.com", "historyId": "1"})
	})
	rateBefore := c.budget.Rate()
	if _, err := c.Profile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.count("profile") != 2 || c.Requests() != 2 {
		t.Errorf("hits %d, requests %d", f.count("profile"), c.Requests())
	}
	found := false
	for _, d := range fc.slept {
		if d >= 17*time.Second {
			found = true
		}
	}
	if !found {
		t.Errorf("Retry-After of 17s not honoured: slept %v", fc.slept)
	}
	if c.budget.Rate() >= rateBefore {
		t.Errorf("a 429 must slow the budget: %v -> %v", rateBefore, c.budget.Rate())
	}
}

func TestRateLimitExhaustsRetries(t *testing.T) {
	f, c, fc := newFake(t)
	f.on("messages.get", func(w http.ResponseWriter, r *http.Request, _ int) {
		apiError(w, 403, "userRateLimitExceeded", nil) // Gmail uses 403 as well as 429
	})
	_, err := c.GetRaw(context.Background(), "m")
	if !errors.Is(err, ErrRateLimited) || Classify(err) != KindRateLimit {
		t.Fatalf("err = %v", err)
	}
	if f.count("messages.get") != 4 {
		t.Errorf("attempts = %d, want 4", f.count("messages.get"))
	}
	// Backoff grows: ~1s, ~2s, ~4s (±25 %).
	var backoffs []time.Duration
	for _, d := range fc.slept {
		if d >= 500*time.Millisecond && d < 10*time.Second { // the 20s account pauses are checked elsewhere
			backoffs = append(backoffs, d)
		}
	}
	if len(backoffs) < 3 || backoffs[1] < backoffs[0] || backoffs[2] < backoffs[1] {
		t.Errorf("backoff did not grow: %v", fc.slept)
	}
}

func TestServerErrorsRetryAndOtherClientErrorsDoNot(t *testing.T) {
	f, c, _ := newFake(t)
	f.on("profile", func(w http.ResponseWriter, r *http.Request, hit int) {
		if hit < 3 {
			apiError(w, 503, "backendError", nil)
			return
		}
		writeJSON(w, map[string]any{"emailAddress": "x", "historyId": "1"})
	})
	if _, err := c.Profile(context.Background()); err != nil {
		t.Fatalf("503s should be retried: %v", err)
	}
	f.on("labels.list", func(w http.ResponseWriter, r *http.Request, _ int) { apiError(w, 400, "invalidArgument", nil) })
	_, err := c.Labels(context.Background())
	if err == nil || f.count("labels.list") != 1 || Classify(err) != KindFetch {
		t.Errorf("400: err %v, hits %d", err, f.count("labels.list"))
	}
}

func TestAuthErrorsAreNotRetried(t *testing.T) {
	for _, code := range []int{401, 403} {
		f, c, fc := newFake(t)
		f.on("profile", func(w http.ResponseWriter, r *http.Request, _ int) {
			apiError(w, code, "authError", nil) // 403 without a rate reason: scope or API disabled
		})
		_, err := c.Profile(context.Background())
		if !errors.Is(err, ErrAuth) || Classify(err) != KindAuth {
			t.Errorf("%d: err = %v", code, err)
		}
		if f.count("profile") != 1 || len(fc.slept) != 0 {
			t.Errorf("%d: hits %d, slept %v", code, f.count("profile"), fc.slept)
		}
	}
}

func TestRevokedRefreshTokenIsAuthError(t *testing.T) {
	// A token endpoint answering invalid_grant, as Google does for a revoked token.
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
	}))
	defer tokenSrv.Close()
	cfg := &oauth2.Config{ClientID: "id", ClientSecret: "s", Endpoint: oauth2.Endpoint{TokenURL: tokenSrv.URL}}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, tokenSrv.Client())
	gm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("API must not be called without a token") }))
	defer gm.Close()

	c, err := NewClient(ctx, Options{HTTPClient: NewHTTPClient(ctx, cfg, "revoked"), Endpoint: gm.URL + "/", MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	fc := newFakeClock()
	c.sleep = fc.sleep
	_, err = c.Profile(ctx)
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v", err)
	}
	if len(fc.slept) != 0 || c.Requests() != 1 {
		t.Errorf("invalid_grant must not be retried (requests %d)", c.Requests())
	}
}

func TestAuthorizationHeaderIsSent(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"at-1","token_type":"Bearer","expires_in":3600}`)
	}))
	defer tokenSrv.Close()
	f := &fakeGmail{t: t, hits: map[string]int{}, handlers: map[string]func(http.ResponseWriter, *http.Request, int){}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	defer f.srv.Close()
	f.on("profile", func(w http.ResponseWriter, r *http.Request, _ int) {
		writeJSON(w, map[string]any{"emailAddress": "x", "historyId": "1"})
	})

	cfg := &oauth2.Config{ClientID: "id", ClientSecret: "s", Endpoint: oauth2.Endpoint{TokenURL: tokenSrv.URL}}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, tokenSrv.Client())
	c, _ := NewClient(ctx, Options{HTTPClient: NewHTTPClient(ctx, cfg, "rt"), Endpoint: f.srv.URL + "/"})
	if _, err := c.Profile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.auth) != 1 || f.auth[0] != "Bearer at-1" {
		t.Errorf("Authorization = %v", f.auth)
	}
}

func TestContextCancelStopsRetrying(t *testing.T) {
	f, c, _ := newFake(t)
	f.on("profile", func(w http.ResponseWriter, r *http.Request, _ int) { apiError(w, 503, "backendError", nil) })
	ctx, cancel := context.WithCancel(context.Background())
	c.sleep = func(ctx context.Context, d time.Duration) error { cancel(); return ctx.Err() }
	_, err := c.Profile(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if f.count("profile") != 1 {
		t.Errorf("hits = %d", f.count("profile"))
	}
}

func TestEveryAttemptSpendsQuota(t *testing.T) {
	f, c, fc := newFake(t)
	fc.advTo = false // the clock stands still, so tokens only go down
	f.on("messages.get", func(w http.ResponseWriter, r *http.Request, hit int) {
		if hit < 3 {
			apiError(w, 500, "backendError", nil)
			return
		}
		writeJSON(w, rawMessage("m", "x"))
	})
	b := c.budget
	b.mu.Lock()
	b.tokens = b.burst
	b.mu.Unlock()
	if _, err := c.GetRaw(context.Background(), "m"); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	spent := b.burst - b.tokens
	b.mu.Unlock()
	if spent != 15 || c.Requests() != 3 { // 3 attempts x 5 units
		t.Errorf("requests = %d spent = %v", c.Requests(), spent)
	}
}

func TestConcurrentCallsShareTheBudget(t *testing.T) {
	f, c, fc := newFake(t)
	fc.advTo = false
	f.on("messages.get", func(w http.ResponseWriter, r *http.Request, _ int) { writeJSON(w, rawMessage("m", "x")) })
	// 150 units/s burst = 30 gets free; 50 concurrent gets must make 20 of them wait.
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = c.GetRaw(context.Background(), "m") }()
	}
	wg.Wait()
	if len(fc.slept) != 20 {
		t.Errorf("waiters = %d, want 20", len(fc.slept))
	}
}

func TestClassifyAndOptions(t *testing.T) {
	if Classify(errors.New("boom")) != KindFetch {
		t.Error("unknown errors are fetch errors")
	}
	if _, err := NewClient(context.Background(), Options{}); err == nil {
		t.Error("missing HTTPClient accepted")
	}
	cfg := OAuthConfig("id", "secret", "https://x/oauth/callback")
	if len(cfg.Scopes) != 1 || cfg.Scopes[0] != "https://www.googleapis.com/auth/gmail.readonly" {
		t.Errorf("scopes = %v", cfg.Scopes)
	}
}

func TestRateLimitPausesTheWholeBudgetAndIsMorePatientThanOtherErrors(t *testing.T) {
	f, c, fc := newFake(t)
	c.rlAttempts = 6
	f.on("messages.get", func(w http.ResponseWriter, r *http.Request, hit int) {
		if hit <= 5 {
			apiError(w, 403, "rateLimitExceeded", nil)
			return
		}
		writeJSON(w, rawMessage("m", "x"))
	})
	if _, err := c.GetRaw(context.Background(), "m"); err != nil {
		t.Fatalf("5 rate limits then success must be survived with 6 attempts: %v", err)
	}
	var total time.Duration
	for _, d := range fc.slept {
		total += d
	}
	if total < 5*20*time.Second {
		t.Errorf("waited only %v in total: each rate limit must pause the account for at least 20s", total)
	}
}
