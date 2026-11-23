package imapdest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

var ctx = context.Background()

func raw(id string) []byte {
	return []byte("From: a@x\r\nSubject: " + id + "\r\nMessage-ID: <" + id + "@x>\r\n\r\nbody of " + id + "\r\n")
}

func msg(id string) Message {
	return Message{ID: HeaderValue("acct", id), Raw: raw(id), Date: time.Date(2024, 3, 5, 12, 0, 0, 0, time.UTC), Flags: []string{`\Seen`}}
}

func open(t *testing.T, f *fakeServer) *Session {
	t.Helper()
	s, err := Open(ctx, f.opts())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func statuses(rs []Result) []Status {
	var out []Status
	for _, r := range rs {
		out = append(out, r.Status)
	}
	return out
}

func TestDeliverToSeveralFoldersCreatesThemAndPreservesContent(t *testing.T) {
	f := newFakeServer()
	s := open(t, f)
	m := msg("m1")
	rs := s.Deliver(ctx, m, []string{"INBOX", "Projects", "Work/Customers"})
	if !reflect.DeepEqual(statuses(rs), []Status{Delivered, Delivered, Delivered}) {
		t.Fatalf("results = %+v", rs)
	}
	if rs[0].Created || !rs[1].Created || !rs[2].Created {
		t.Errorf("created flags = %v %v %v", rs[0].Created, rs[1].Created, rs[2].Created)
	}
	for _, folder := range []string{"INBOX", "Projects", "Work/Customers"} {
		got := f.folders[folder].msgs
		if len(got) != 1 {
			t.Fatalf("%s holds %d messages", folder, len(got))
		}
		if !bytes.Equal(got[0].raw, append([]byte("X-Gmail-Archive-ID: acct.m1\r\n"), m.Raw...)) {
			t.Errorf("%s: content altered: %q", folder, got[0].raw)
		}
		if !got[0].date.Equal(m.Date) || !reflect.DeepEqual(got[0].flags, m.Flags) {
			t.Errorf("%s: date/flags = %v %v", folder, got[0].date, got[0].flags)
		}
	}
	if !f.folders["Projects"].subscribed {
		t.Error("created folders must be subscribed")
	}
	st := s.Stats()
	if st.Delivered != 3 || st.FoldersCreated != 2 || st.Skipped != 0 { // INBOX existed; the parent "Work" is made by the server, not counted
		t.Errorf("stats = %+v", st)
	}
	if m.Raw[0] != 'F' {
		t.Error("caller's message was modified")
	}
}

func TestRedeliveryIsSkippedAcrossRestarts(t *testing.T) {
	f := newFakeServer()
	s := open(t, f)
	s.Deliver(ctx, msg("m1"), []string{"INBOX", "Projects"})

	// Same session.
	rs := s.Deliver(ctx, msg("m1"), []string{"INBOX", "Projects"})
	if !reflect.DeepEqual(statuses(rs), []Status{Skipped, Skipped}) {
		t.Errorf("same session: %v", statuses(rs))
	}
	// A new process knows nothing but what the server holds: the crash
	// recovery path of the spec (APPEND done, checkpoint not saved).
	s2 := open(t, f)
	rs = s2.Deliver(ctx, msg("m1"), []string{"INBOX", "Projects"})
	if !reflect.DeepEqual(statuses(rs), []Status{Skipped, Skipped}) {
		t.Errorf("after restart: %v", statuses(rs))
	}
	if f.count("INBOX") != 1 || f.count("Projects") != 1 {
		t.Errorf("duplicates: INBOX %d, Projects %d", f.count("INBOX"), f.count("Projects"))
	}
}

func TestDedupIsPerFolder(t *testing.T) {
	f := newFakeServer()
	s := open(t, f)
	s.Deliver(ctx, msg("m1"), []string{"INBOX"})
	// The same message gaining a label later still reaches the new folder.
	rs := s.Deliver(ctx, msg("m1"), []string{"INBOX", "Finance"})
	if !reflect.DeepEqual(statuses(rs), []Status{Skipped, Delivered}) {
		t.Errorf("results = %v", statuses(rs))
	}
}

func TestDedupIsPerAccount(t *testing.T) {
	f := newFakeServer()
	s := open(t, f)
	a := Message{ID: HeaderValue("acctA", "m1"), Raw: raw("m1"), Date: time.Now()}
	b := Message{ID: HeaderValue("acctB", "m1"), Raw: raw("m1"), Date: time.Now()}
	s.Deliver(ctx, a, []string{"INBOX"})
	if rs := s.Deliver(ctx, b, []string{"INBOX"}); rs[0].Status != Delivered {
		t.Errorf("another account's message with the same Gmail ID must be delivered: %v", rs[0])
	}
}

func TestPartialDeliveryReportsPerFolderAndRetryOnlyFillsTheGap(t *testing.T) {
	f := newFakeServer()
	f.appendHook = func(folder string, n int) (bool, error) {
		if folder == "Finance" && n == 1 {
			return false, errNoSpace
		}
		return true, nil
	}
	s := open(t, f)
	rs := s.Deliver(ctx, msg("m1"), []string{"INBOX", "Finance", "Projects"})
	if !reflect.DeepEqual(statuses(rs), []Status{Delivered, Failed, Delivered}) {
		t.Fatalf("results = %v", statuses(rs))
	}
	if KindOf(rs[1].Err) != KindAppend || !Retryable(rs[1].Err) {
		t.Errorf("error kind = %v", KindOf(rs[1].Err))
	}
	// Retry the whole message: finished folders are skipped, the failed one is filled.
	rs = s.Deliver(ctx, msg("m1"), []string{"INBOX", "Finance", "Projects"})
	if !reflect.DeepEqual(statuses(rs), []Status{Skipped, Delivered, Skipped}) {
		t.Errorf("retry = %v", statuses(rs))
	}
	for _, folder := range []string{"INBOX", "Finance", "Projects"} {
		if f.count(folder) != 1 {
			t.Errorf("%s holds %d", folder, f.count(folder))
		}
	}
}

func TestConnectionLostAfterAppendDoesNotDuplicate(t *testing.T) {
	f := newFakeServer()
	// The server stored the message but the response never arrived.
	f.appendHook = func(folder string, n int) (bool, error) {
		if n == 1 {
			return true, errConnLost
		}
		return true, nil
	}
	s := open(t, f)
	rs := s.Deliver(ctx, msg("m1"), []string{"INBOX"})
	if rs[0].Status != Skipped || rs[0].Err != nil {
		t.Fatalf("result = %+v", rs[0])
	}
	if f.count("INBOX") != 1 {
		t.Fatalf("INBOX holds %d copies", f.count("INBOX"))
	}
}

func TestConnectionLostBeforeAppendIsRetriedOnce(t *testing.T) {
	f := newFakeServer()
	f.appendHook = func(folder string, n int) (bool, error) {
		if n == 1 {
			return false, errConnLost // never reached the server
		}
		return true, nil
	}
	s := open(t, f)
	rs := s.Deliver(ctx, msg("m1"), []string{"INBOX"})
	if rs[0].Status != Delivered || f.count("INBOX") != 1 {
		t.Fatalf("result = %+v, count %d", rs[0], f.count("INBOX"))
	}
	if f.dials < 2 {
		t.Error("a broken connection must be replaced")
	}
}

func TestPersistentConnectionLossFailsAsConnectionError(t *testing.T) {
	f := newFakeServer()
	f.appendHook = func(string, int) (bool, error) { return false, errConnLost }
	s := open(t, f)
	rs := s.Deliver(ctx, msg("m1"), []string{"INBOX"})
	if rs[0].Status != Failed || KindOf(rs[0].Err) != KindConnection {
		t.Fatalf("result = %+v (kind %v)", rs[0], KindOf(rs[0].Err))
	}
	if f.appends["INBOX"] != 2 {
		t.Errorf("append attempts = %d, want 2", f.appends["INBOX"])
	}
}

func TestNonConnectionAppendErrorsAreNotRetriedInline(t *testing.T) {
	f := newFakeServer()
	f.appendHook = func(string, int) (bool, error) { return false, errNoSpace }
	s := open(t, f)
	rs := s.Deliver(ctx, msg("m1"), []string{"INBOX"})
	if rs[0].Status != Failed || f.appends["INBOX"] != 1 || KindOf(rs[0].Err) != KindAppend {
		t.Errorf("result = %+v, attempts %d", rs[0], f.appends["INBOX"])
	}
	var ie *imap.Error
	if !errors.As(rs[0].Err, &ie) || ie.Code != imap.ResponseCodeOverQuota {
		t.Error("the IMAP error must stay reachable for classification")
	}
}

func TestUIDValidityChangeDropsTheIndex(t *testing.T) {
	f := newFakeServer()
	s := open(t, f)
	s.Deliver(ctx, msg("m1"), []string{"INBOX"})
	if f.loadCalls["INBOX"] != 1 {
		t.Fatalf("loads = %d", f.loadCalls["INBOX"])
	}
	// The folder is recreated behind our back: UIDVALIDITY differs on the next APPEND.
	f.mu.Lock()
	f.folders["INBOX"].uidValidity = 99
	f.mu.Unlock()
	s.Deliver(ctx, msg("m2"), []string{"INBOX"})
	s.Deliver(ctx, msg("m3"), []string{"INBOX"})
	if f.loadCalls["INBOX"] != 2 {
		t.Errorf("index should have been reloaded once after the change, loads = %d", f.loadCalls["INBOX"])
	}
}

func TestOverflowFallsBackToServerSearch(t *testing.T) {
	f := newFakeServer()
	o := f.opts()
	o.IndexMaxIDs = 2
	s0, _ := Open(ctx, f.opts())
	for _, id := range []string{"a", "b", "c"} {
		s0.Deliver(ctx, msg(id), []string{"Big"})
	}
	s, err := Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rs := s.Deliver(ctx, msg("b"), []string{"Big"})
	if rs[0].Status != Skipped {
		t.Errorf("existing message in an overflowing folder: %v", rs[0])
	}
	rs = s.Deliver(ctx, msg("d"), []string{"Big"})
	if rs[0].Status != Delivered || f.count("Big") != 4 {
		t.Errorf("new message: %v, count %d", rs[0], f.count("Big"))
	}
	if f.hasCalls == 0 {
		t.Error("overflow mode must search on the server")
	}
	if f.loadCalls["Big"] > 2 { // once by s0, once here; never per message
		t.Errorf("index loads = %d; overflow must not reload on every message", f.loadCalls["Big"])
	}
}

func TestParentsCreatedWhenServerDoesNotDoItImplicitly(t *testing.T) {
	f := newFakeServer()
	f.autoParent = false
	s := open(t, f)
	rs := s.Deliver(ctx, msg("m1"), []string{"A/B/C"})
	if rs[0].Status != Delivered {
		t.Fatalf("result = %+v", rs[0])
	}
	for _, n := range []string{"A", "A/B", "A/B/C"} {
		if _, ok := f.folders[n]; !ok {
			t.Errorf("folder %q missing: %v", n, f.folderNames())
		}
	}
}

func TestExistingFoldersAreNotRecreated(t *testing.T) {
	f := newFakeServer()
	f.folders["Archive"] = &fakeFolder{uidValidity: 1}
	s := open(t, f)
	rs := s.Deliver(ctx, msg("m1"), []string{"Archive", "inbox"})
	if rs[0].Created || s.Stats().FoldersCreated != 0 {
		t.Errorf("Archive already existed: %+v stats %+v", rs[0], s.Stats())
	}
	// IMAP folds the case of INBOX only.
	if rs[1].Folder != "INBOX" || rs[1].Status != Delivered || f.count("INBOX") != 1 {
		t.Errorf("inbox normalisation: %+v", rs[1])
	}
}

func TestConcurrentDeliveryRespectsConnectionLimitAndDedupes(t *testing.T) {
	f := newFakeServer()
	s := open(t, f)
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Deliver(ctx, msg(fmt.Sprintf("m%d", i)), []string{"INBOX", "Projects", "Work/Customers"})
		}()
	}
	wg.Wait()
	for _, folder := range []string{"INBOX", "Projects", "Work/Customers"} {
		if f.count(folder) != 60 {
			t.Errorf("%s holds %d, want 60", folder, f.count(folder))
		}
	}
	if f.maxLive > 3 {
		t.Errorf("up to %d connections open, limit is 3", f.maxLive)
	}
	if f.loadCalls["INBOX"] != 1 {
		t.Errorf("INBOX index loaded %d times", f.loadCalls["INBOX"])
	}
}

func TestDialFailureAndCancellation(t *testing.T) {
	f := newFakeServer()
	f.dialErr = &OpError{Kind: KindAuth, Op: "login", Err: errors.New("bad password")}
	if _, err := Open(ctx, f.opts()); KindOf(err) != KindAuth || Retryable(err) {
		t.Errorf("Open err = %v (kind %v, retryable %v)", err, KindOf(err), Retryable(err))
	}

	f = newFakeServer()
	s := open(t, f)
	c, cancel := context.WithCancel(ctx)
	cancel()
	rs := s.Deliver(c, msg("m1"), []string{"INBOX"})
	if rs[0].Status != Failed || !errors.Is(rs[0].Err, context.Canceled) {
		t.Errorf("cancelled delivery = %+v", rs[0])
	}
	if f.count("INBOX") != 0 {
		t.Error("nothing may be appended after cancellation")
	}
}

func TestErrorClassification(t *testing.T) {
	cases := []struct {
		err   error
		kind  Kind
		retry bool
	}{
		{&OpError{Kind: KindAuth, Err: errors.New("x")}, KindAuth, false},
		{errConnLost, KindConnection, true},
		{fmt.Errorf("wrap: %w", errBroken), KindConnection, true},
		{&imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeNoPerm}, KindAppend, false},
		{&imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeOverQuota}, KindAppend, true},
		{&imap.Error{Type: imap.StatusResponseTypeBye}, KindConnection, true},
		{&OpError{Kind: KindFolder, Err: errors.New("x")}, KindFolder, true},
	}
	for _, tc := range cases {
		if KindOf(tc.err) != tc.kind || Retryable(tc.err) != tc.retry {
			t.Errorf("%v: kind %v retry %v, want %v %v", tc.err, KindOf(tc.err), Retryable(tc.err), tc.kind, tc.retry)
		}
	}
}

func TestMalformedMessageFailsAsParseErrorWithoutTouchingTheServer(t *testing.T) {
	f := newFakeServer()
	s := open(t, f)
	m := msg("m1")
	m.Raw = []byte(" starts with whitespace\r\n\r\nbody")
	rs := s.Deliver(ctx, m, []string{"INBOX", "Projects"})
	for _, r := range rs {
		if r.Status != Failed || KindOf(r.Err) != KindParse || Retryable(r.Err) {
			t.Errorf("result = %+v", r)
		}
	}
	if f.count("INBOX") != 0 || len(f.folders) != 1 {
		t.Error("the server must not be touched")
	}
}

func TestSessionWideIndexBudgetFallsBackToSearch(t *testing.T) {
	f := newFakeServer()
	seed, _ := Open(ctx, f.opts())
	for _, id := range []string{"a", "b", "c"} {
		seed.Deliver(ctx, msg(id), []string{"One", "Two"})
	}
	o := f.opts()
	o.IndexMaxTotal = 4 // room for one folder of 3, not for two
	s, err := Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rs := s.Deliver(ctx, msg("a"), []string{"One", "Two"})
	if rs[0].Status != Skipped || rs[1].Status != Skipped {
		t.Fatalf("results = %v", statuses(rs))
	}
	if f.hasCalls == 0 {
		t.Error("the second folder must be checked by searching once the budget is spent")
	}
	if rs = s.Deliver(ctx, msg("z"), []string{"One", "Two"}); rs[0].Status != Delivered || rs[1].Status != Delivered {
		t.Errorf("new message: %v", statuses(rs))
	}
	if got := s.indexed.Load(); got > 4 {
		t.Errorf("indexed %d IDs, budget is 4", got)
	}
}

func TestFolderNameRefusedByTheServerFallsBackToASanitisedName(t *testing.T) {
	f := newFakeServer()
	f.rejectDot = true // Maildir++ does not allow "." in names
	s := open(t, f)
	m := msg("m1")
	rs := s.Deliver(ctx, m, []string{"Project 1.0", "Plain", "A Processer .../Newsletters"})
	if !reflect.DeepEqual(statuses(rs), []Status{Delivered, Delivered, Delivered}) {
		t.Fatalf("results = %+v", rs)
	}
	if rs[0].Requested != "Project 1.0" || rs[0].Folder != "Project 1_0" || !rs[0].Created {
		t.Errorf("first = %+v", rs[0])
	}
	if rs[1].Folder != "Plain" || rs[1].Requested != "Plain" {
		t.Errorf("an acceptable name must be left alone: %+v", rs[1])
	}
	if rs[2].Folder != "A Processer ___/Newsletters" {
		t.Errorf("third = %+v", rs[2])
	}
	if f.count("Project 1_0") != 1 || f.count("Project 1.0") != 0 {
		t.Errorf("folders = %v", f.folderNames())
	}
	if got := s.Renames(); got["Project 1.0"] != "Project 1_0" || len(got) != 2 {
		t.Errorf("renames = %v", got)
	}

	// Same session again, and a new process: both find the earlier copies in the sanitised folder.
	f.mu.Lock()
	before := f.creates
	f.mu.Unlock()
	s2 := open(t, f)
	rs = s2.Deliver(ctx, m, []string{"Project 1.0", "Plain", "A Processer .../Newsletters"})
	if !reflect.DeepEqual(statuses(rs), []Status{Skipped, Skipped, Skipped}) {
		t.Errorf("redelivery = %v", statuses(rs))
	}
	f.mu.Lock()
	extra := f.creates - before
	f.mu.Unlock()
	if extra != 0 {
		t.Errorf("a later session retried %d refused CREATEs: it should find the sanitised folder", extra)
	}
	if f.count("Project 1_0") != 1 {
		t.Error("duplicate copy")
	}
}

func TestOtherCreateErrorsAreNotRetriedUnderAnotherName(t *testing.T) {
	f := newFakeServer()
	f.autoParent = false // a missing parent, not a name problem: handled by creating parents
	s := open(t, f)
	if rs := s.Deliver(ctx, msg("m1"), []string{"A/B"}); rs[0].Status != Delivered || rs[0].Folder != "A/B" {
		t.Errorf("result = %+v", rs[0])
	}
	// A name the server refuses in every spelling fails clearly instead of looping.
	f2 := newFakeServer()
	f2.rejectDot = true
	s2 := open(t, f2)
	f2.mu.Lock()
	f2.creates = 0
	f2.mu.Unlock()
	rs := s2.Deliver(ctx, msg("m1"), []string{"x.y"})
	if rs[0].Status != Delivered || rs[0].Folder != "x_y" {
		t.Errorf("result = %+v", rs[0])
	}
	if f2.creates > 3 {
		t.Errorf("CREATE attempts = %d", f2.creates)
	}
}
