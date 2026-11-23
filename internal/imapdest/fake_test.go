package imapdest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
)

// fakeServer is an in-memory IMAP destination that several sessions (and so
// "restarts") can share. Failure injection is by hooks.
type fakeServer struct {
	mu         sync.Mutex
	delim      string
	folders    map[string]*fakeFolder
	autoParent bool // CREATE a/b also creates a (Dovecot) or fails (false)
	rejectDot  bool // CREATE refuses names containing "." (Maildir++)
	creates    int

	dials      int
	liveConns  int
	maxLive    int
	dialErr    error
	appendHook func(folder string, n int) (applied bool, err error) // n counts appends to folder
	appends    map[string]int
	loadCalls  map[string]int
	hasCalls   int
	forceUIDV  map[string]uint32
}

type fakeMsg struct {
	raw   []byte
	date  time.Time
	flags []string
}

type fakeFolder struct {
	uidValidity uint32
	msgs        []fakeMsg
	subscribed  bool
}

func newFakeServer() *fakeServer {
	return &fakeServer{delim: "/", folders: map[string]*fakeFolder{"INBOX": {uidValidity: 1}},
		autoParent: true, appends: map[string]int{}, loadCalls: map[string]int{}, forceUIDV: map[string]uint32{}}
}

func (f *fakeServer) dial(ctx context.Context) (conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dialErr != nil {
		return nil, f.dialErr
	}
	f.dials++
	f.liveConns++
	if f.liveConns > f.maxLive {
		f.maxLive = f.liveConns
	}
	return &fakeConn{srv: f}, nil
}

func (f *fakeServer) opts() Options { return Options{Dial: f.dial, Conns: 3, Subscribe: true} }

func (f *fakeServer) count(folder string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fo := f.folders[folder]; fo != nil {
		return len(fo.msgs)
	}
	return 0
}

func (f *fakeServer) folderNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for n := range f.folders {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

type fakeConn struct {
	srv    *fakeServer
	broken bool
	closed bool
}

func (c *fakeConn) Delimiter(context.Context) (string, error) { return c.srv.delim, nil }
func (c *fakeConn) Broken() bool                              { return c.broken }
func (c *fakeConn) Close() error {
	c.srv.mu.Lock()
	defer c.srv.mu.Unlock()
	if !c.closed {
		c.closed = true
		c.srv.liveConns--
	}
	return nil
}

func (c *fakeConn) ListFolders(context.Context) ([]string, error) { return c.srv.folderNames(), nil }

func (c *fakeConn) CreateFolder(_ context.Context, folder string, subscribe bool) (bool, error) {
	s := c.srv
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creates++
	if s.rejectDot && strings.Contains(folder, ".") {
		return false, &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeCannot, Text: "Character not allowed in mailbox name: '.'"}
	}
	if _, ok := s.folders[folder]; ok {
		return false, nil
	}
	if i := strings.LastIndex(folder, s.delim); i > 0 {
		parent := folder[:i]
		if _, ok := s.folders[parent]; !ok {
			if !s.autoParent {
				return false, &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeNonExistent, Text: "parent missing"}
			}
			s.folders[parent] = &fakeFolder{uidValidity: 1}
		}
	}
	s.folders[folder] = &fakeFolder{uidValidity: 1, subscribed: subscribe}
	return true, nil
}

func (c *fakeConn) LoadIndex(_ context.Context, folder string, maxIDs int) (Index, error) {
	s := c.srv
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadCalls[folder]++
	fo := s.folders[folder]
	if fo == nil {
		return Index{}, &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeNonExistent}
	}
	uv := fo.uidValidity
	if v, ok := s.forceUIDV[folder]; ok {
		uv = v
	}
	idx := Index{UIDValidity: uv, IDs: map[string]struct{}{}}
	for _, m := range fo.msgs {
		for _, v := range headerValues(headerBlock(m.raw)) {
			idx.IDs[v] = struct{}{}
		}
	}
	if len(idx.IDs) > maxIDs {
		return Index{UIDValidity: uv, Overflow: true}, nil
	}
	return idx, nil
}

func (c *fakeConn) HasID(_ context.Context, folder, value string) (bool, error) {
	s := c.srv
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hasCalls++
	fo := s.folders[folder]
	if fo == nil {
		return false, &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeNonExistent}
	}
	for _, m := range fo.msgs {
		for _, v := range headerValues(headerBlock(m.raw)) {
			if v == value {
				return true, nil
			}
		}
	}
	return false, nil
}

func (c *fakeConn) Append(_ context.Context, folder string, raw []byte, flags []string, date time.Time) (AppendResult, error) {
	s := c.srv
	s.mu.Lock()
	defer s.mu.Unlock()
	fo := s.folders[folder]
	if fo == nil {
		return AppendResult{}, &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeTryCreate}
	}
	s.appends[folder]++
	if s.appendHook != nil {
		applied, err := s.appendHook(folder, s.appends[folder])
		if applied {
			fo.msgs = append(fo.msgs, fakeMsg{append([]byte(nil), raw...), date, flags})
		}
		if err != nil {
			if isConnectionError(err) {
				c.broken = true
			}
			return AppendResult{}, err
		}
		if applied {
			return AppendResult{UIDValidity: fo.uidValidity, UID: uint32(len(fo.msgs))}, nil
		}
	}
	fo.msgs = append(fo.msgs, fakeMsg{append([]byte(nil), raw...), date, flags})
	return AppendResult{UIDValidity: fo.uidValidity, UID: uint32(len(fo.msgs))}, nil
}

var (
	errConnLost = fmt.Errorf("write: %w", io.ErrUnexpectedEOF)
	errNoSpace  = &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeOverQuota, Text: "quota"}
)

var _ = errors.New
