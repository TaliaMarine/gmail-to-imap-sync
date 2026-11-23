package imapdest

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

// startMemServer runs go-imap's in-memory server on localhost, so the real
// Conn and Session are exercised over the wire protocol without Docker.
func startMemServer(t *testing.T) Config {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("archive", "secret")
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
	return Config{Host: host, Port: p, TLSMode: TLSNone, Username: "archive", Password: "secret", OpTimeout: 10 * time.Second}
}

func TestConnAgainstWireProtocol(t *testing.T) {
	cfg := startMemServer(t)
	c, err := Dial(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	delim, err := c.Delimiter(ctx)
	if err != nil || delim != "/" {
		t.Fatalf("delimiter = %q, %v", delim, err)
	}
	created, err := c.CreateFolder(ctx, "Projects", true)
	if err != nil || !created {
		t.Fatalf("create = %v, %v", created, err)
	}
	created, err = c.CreateFolder(ctx, "Projects", true)
	if err != nil || created {
		t.Fatalf("creating twice = %v, %v; must be tolerated", created, err)
	}
	names, err := c.ListFolders(ctx)
	if err != nil || !contains(names, "INBOX") || !contains(names, "Projects") {
		t.Fatalf("folders = %v, %v", names, err)
	}

	date := time.Date(2020, 2, 3, 4, 5, 6, 0, time.UTC)
	body, _ := AddHeader(raw("m1"), HeaderValue("acct", "m1"))
	res, err := c.Append(ctx, "Projects", body, []string{`\Seen`}, date)
	if err != nil || res.UID == 0 || res.UIDValidity == 0 {
		t.Fatalf("append = %+v, %v", res, err)
	}
	// A message without the header, and one whose ID merely contains ours.
	if _, err := c.Append(ctx, "Projects", raw("plain"), nil, date); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Append(ctx, "Projects", mustHeader(t, raw("m10"), HeaderValue("acct", "m10")), nil, date); err != nil {
		t.Fatal(err)
	}

	idx, err := c.LoadIndex(ctx, "Projects", 100)
	if err != nil {
		t.Fatal(err)
	}
	if idx.UIDValidity != res.UIDValidity || len(idx.IDs) != 2 {
		t.Fatalf("index = %+v", idx)
	}
	for _, id := range []string{"acct.m1", "acct.m10"} {
		if _, ok := idx.IDs[id]; !ok {
			t.Errorf("index lacks %s: %v", id, idx.IDs)
		}
	}

	// Exactness: SEARCH HEADER is a substring match, "acct.m1" is inside "acct.m10".
	if has, err := c.HasID(ctx, "Projects", "acct.m1"); err != nil || !has {
		t.Errorf("HasID m1 = %v, %v", has, err)
	}
	if has, err := c.HasID(ctx, "Projects", "acct.m"); err != nil || has {
		t.Errorf("HasID of a mere prefix = %v, %v; must be false", has, err)
	}
	if has, err := c.HasID(ctx, "Projects", "acct.zzz"); err != nil || has {
		t.Errorf("HasID absent = %v, %v", has, err)
	}

	// Overflow switches to search mode instead of holding the IDs.
	big, err := c.LoadIndex(ctx, "Projects", 1)
	if err != nil || !big.Overflow || big.IDs != nil {
		t.Errorf("overflow index = %+v, %v", big, err)
	}

	// An empty folder.
	if _, err := c.CreateFolder(ctx, "Empty", false); err != nil {
		t.Fatal(err)
	}
	if e, err := c.LoadIndex(ctx, "Empty", 10); err != nil || len(e.IDs) != 0 {
		t.Errorf("empty index = %+v, %v", e, err)
	}
	// A missing folder is an error, not an empty index.
	if _, err := c.LoadIndex(ctx, "Nope", 10); err == nil {
		t.Error("missing folder accepted")
	}
}

func TestSessionOverWireProtocol(t *testing.T) {
	cfg := startMemServer(t)
	s, err := Open(ctx, Options{Config: cfg, Conns: 2, Subscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Delimiter() != "/" {
		t.Errorf("delimiter = %q", s.Delimiter())
	}
	m := msg("m1")
	rs := s.Deliver(ctx, m, []string{"INBOX", "Work/Customers"})
	if rs[0].Status != Delivered || rs[1].Status != Delivered || !rs[1].Created {
		t.Fatalf("results = %+v", rs)
	}
	s2, err := Open(ctx, Options{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	rs = s2.Deliver(ctx, m, []string{"INBOX", "Work/Customers"})
	if rs[0].Status != Skipped || rs[1].Status != Skipped {
		t.Errorf("a new session must find the earlier copies: %+v", rs)
	}
}

func TestLoginFailureIsAuthError(t *testing.T) {
	cfg := startMemServer(t)
	cfg.Password = "wrong"
	_, err := Dial(ctx, cfg)
	if KindOf(err) != KindAuth || Retryable(err) {
		t.Fatalf("err = %v (kind %v)", err, KindOf(err))
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Error("password leaked into the error")
	}
}

func TestUnreachableServerIsConnectionError(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()
	p, _ := strconv.Atoi(port)
	_, err := Dial(ctx, Config{Host: host, Port: p, TLSMode: TLSNone, DialTimeout: time.Second})
	if KindOf(err) != KindConnection || !Retryable(err) {
		t.Errorf("err = %v (kind %v)", err, KindOf(err))
	}
}

func TestCancelledContextClosesConnection(t *testing.T) {
	cfg := startMemServer(t)
	c, err := Dial(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.ListFolders(cx); err == nil {
		t.Error("cancelled call succeeded")
	}
	// A timeout mid-command breaks the connection so the pool discards it.
	c2, _ := Dial(ctx, cfg)
	c2.cfg.OpTimeout = time.Nanosecond
	if _, err := c2.ListFolders(ctx); err == nil || !c2.Broken() {
		t.Errorf("timed out command: err %v, broken %v", err, c2.Broken())
	}
	if _, err := c2.ListFolders(ctx); !isConnectionError(err) {
		t.Errorf("a broken connection must refuse further commands: %v", err)
	}
}

func TestCheckConnection(t *testing.T) {
	cfg := startMemServer(t)
	folders, delim, err := CheckConnection(ctx, cfg)
	if err != nil || delim != "/" || !contains(folders, "INBOX") {
		t.Errorf("CheckConnection = %v %q %v", folders, delim, err)
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

func mustHeader(t *testing.T, raw []byte, v string) []byte {
	t.Helper()
	out, err := AddHeader(raw, v)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
