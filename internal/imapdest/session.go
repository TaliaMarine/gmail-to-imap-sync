package imapdest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/emersion/go-imap/v2"
)

// Message is one Gmail message to deliver.
type Message struct {
	ID    string    // archive header value, see HeaderValue
	Raw   []byte    // exact message bytes from Gmail
	Date  time.Time // Gmail internalDate, becomes the IMAP INTERNALDATE
	Flags []string  // IMAP flags, e.g. \Seen
}

// Status is the outcome of delivering a message to one folder.
type Status int

const (
	// Delivered: the copy was appended.
	Delivered Status = iota + 1
	// Skipped: the folder already holds this message.
	Skipped
	// Failed: see Result.Err.
	Failed
)

func (s Status) String() string {
	switch s {
	case Delivered:
		return "delivered"
	case Skipped:
		return "skipped"
	case Failed:
		return "failed"
	}
	return "unknown"
}

// Result is the outcome for one folder. Folders succeed or fail
// independently, so a partial delivery is visible and retryable.
type Result struct {
	// Folder is the folder as the server knows it. Requested is the name asked
	// for; it differs when the server refused it (for example Maildir++
	// forbids "." in names) and a sanitised name was used instead.
	Requested string
	Folder    string
	Status    Status
	Created   bool // the folder was created for this delivery
	Err       error
}

// Stats are cumulative counters of a session.
type Stats struct {
	FoldersCreated int64
	Delivered      int64
	Skipped        int64
	BytesDelivered int64
}

// Options configure a Session.
type Options struct {
	// Config is used to dial unless Dial is set.
	Config Config
	// Dial overrides how connections are made (tests).
	Dial func(ctx context.Context) (conn, error)
	// Conns is the maximum number of concurrent connections; default 4.
	Conns int
	// IndexMaxIDs caps the archive IDs held in memory for one folder; above
	// it that folder falls back to a server-side search per message.
	// Default 2,000,000.
	IndexMaxIDs int
	// IndexMaxTotal caps the IDs held for all folders of the session together
	// (the same message sits in several folders); folders loaded after the
	// budget is used up fall back to searching. Default 4,000,000 (about
	// 400 MB).
	IndexMaxTotal int
	// Subscribe subscribes to folders the session creates.
	Subscribe bool
}

// Session delivers messages to one destination mailbox. It is safe for
// concurrent use; the engine must deliver a given message from one goroutine
// at a time (a message ID is never delivered concurrently).
type Session struct {
	opts  Options
	slots chan struct{} // connection limit

	mu      sync.Mutex
	idle    []conn
	delim   string
	known   map[string]bool   // folders known to exist
	renamed map[string]string // requested -> used, for names the server refused
	folders map[string]*folderState

	created, delivered, skipped, bytes atomic.Int64
	indexed                            atomic.Int64 // IDs held in all folder indexes
}

type folderState struct {
	mu       sync.Mutex
	ready    bool   // the folder exists
	actual   string // the name it exists under (differs when the server rejected the original)
	idx      *Index // nil until loaded (or after invalidation)
	overflow bool   // too large for an in-memory index
}

// Open connects, discovers the hierarchy delimiter and lists existing folders.
func Open(ctx context.Context, o Options) (*Session, error) {
	if o.Conns <= 0 {
		o.Conns = 4
	}
	if o.IndexMaxIDs <= 0 {
		o.IndexMaxIDs = 2_000_000
	}
	if o.IndexMaxTotal <= 0 {
		o.IndexMaxTotal = 4_000_000
	}
	if o.Dial == nil {
		cfg := o.Config
		o.Dial = func(ctx context.Context) (conn, error) { return Dial(ctx, cfg) }
	}
	s := &Session{
		opts:    o,
		slots:   make(chan struct{}, o.Conns),
		known:   map[string]bool{},
		renamed: map[string]string{},
		folders: map[string]*folderState{},
	}
	c, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer s.release(c)
	if s.delim, err = c.Delimiter(ctx); err != nil {
		return nil, &OpError{Kind: kindFor(err, KindFolder), Op: "list delimiter", Err: err}
	}
	names, err := c.ListFolders(ctx)
	if err != nil {
		return nil, &OpError{Kind: kindFor(err, KindFolder), Op: "list folders", Err: err}
	}
	for _, n := range names {
		s.known[normalise(n)] = true
	}
	return s, nil
}

// Delimiter is the server's hierarchy delimiter, for FolderName in the
// mapping package.
func (s *Session) Delimiter() string { return s.delim }

// Stats returns the cumulative counters.
func (s *Session) Stats() Stats {
	return Stats{
		FoldersCreated: s.created.Load(), Delivered: s.delivered.Load(),
		Skipped: s.skipped.Load(), BytesDelivered: s.bytes.Load(),
	}
}

// Close closes all idle connections.
func (s *Session) Close() error {
	s.mu.Lock()
	idle := s.idle
	s.idle = nil
	s.mu.Unlock()
	var errs []error
	for _, c := range idle {
		errs = append(errs, c.Close())
	}
	return errors.Join(errs...)
}

// Deliver copies m into each folder. The archive header is added once; the
// rest of the message is untouched. It returns one Result per folder, in
// order. Folders that already hold the message are skipped, which is what
// makes crash recovery safe: the next attempt finds the earlier copies.
func (s *Session) Deliver(ctx context.Context, m Message, folders []string) []Result {
	out := make([]Result, 0, len(folders))
	raw, err := AddHeader(m.Raw, m.ID)
	if err != nil {
		for _, f := range folders {
			out = append(out, Result{Folder: normaliseName(f), Status: Failed, Err: &OpError{Kind: KindParse, Op: "prepare", Folder: f, Err: err}})
		}
		return out
	}
	for _, f := range folders {
		out = append(out, s.deliverOne(ctx, m, raw, f))
	}
	return out
}

func (s *Session) deliverOne(ctx context.Context, m Message, raw []byte, folder string) Result {
	folder = normaliseName(folder)
	r := Result{Folder: folder}
	fail := func(err error) Result { r.Status, r.Err = Failed, err; return r }

	st := s.state(folder)
	created, err := s.ensureFolder(ctx, st, folder)
	if err != nil {
		return fail(err)
	}
	r.Created = created
	st.mu.Lock()
	r.Requested, folder, r.Folder = folder, st.actual, st.actual // the server's name from here on
	st.mu.Unlock()

	present, err := s.present(ctx, st, folder, m.ID)
	if err != nil {
		return fail(err)
	}
	if present {
		s.skipped.Add(1)
		r.Status = Skipped
		return r
	}

	res, err := s.appendWithRecovery(ctx, folder, m, raw)
	if err != nil {
		return fail(&OpError{Kind: kindFor(err, KindAppend), Op: "append", Folder: folder, Err: err})
	}
	if res.alreadyThere {
		s.skipped.Add(1)
		r.Status = Skipped
		return r
	}
	s.noteDelivered(st, m.ID, res.UIDValidity)
	s.delivered.Add(1)
	s.bytes.Add(int64(len(raw)))
	r.Status = Delivered
	return r
}

type appendOutcome struct {
	AppendResult
	alreadyThere bool
}

// appendWithRecovery appends and, if the connection broke mid-APPEND (so the
// message may or may not have arrived), checks for the copy on a fresh
// connection before trying once more. This avoids duplicates without
// weakening delivery.
func (s *Session) appendWithRecovery(ctx context.Context, folder string, m Message, raw []byte) (appendOutcome, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		c, err := s.acquire(ctx)
		if err != nil {
			return appendOutcome{}, err
		}
		if attempt > 0 {
			// The first attempt's fate is unknown.
			has, herr := c.HasID(ctx, folder, m.ID)
			if herr != nil {
				s.release(c)
				return appendOutcome{}, herr
			}
			if has {
				s.release(c)
				return appendOutcome{alreadyThere: true}, nil
			}
		}
		res, err := c.Append(ctx, folder, raw, m.Flags, m.Date)
		s.release(c)
		if err == nil {
			return appendOutcome{AppendResult: res}, nil
		}
		lastErr = err
		if !isConnectionError(err) || isCtxErr(err) {
			return appendOutcome{}, err
		}
	}
	return appendOutcome{}, lastErr
}

// present reports whether folder already holds id, loading the folder's
// index on first use.
func (s *Session) present(ctx context.Context, st *folderState, folder, id string) (bool, error) {
	st.mu.Lock()
	if st.idx == nil && !st.overflow {
		if err := s.loadIndexLocked(ctx, st, folder); err != nil {
			st.mu.Unlock()
			return false, err
		}
	}
	if st.idx != nil {
		_, ok := st.idx.IDs[id]
		st.mu.Unlock()
		return ok, nil
	}
	st.mu.Unlock()

	// Overflow: ask the server.
	c, err := s.acquire(ctx)
	if err != nil {
		return false, err
	}
	defer s.release(c)
	has, err := c.HasID(ctx, folder, id)
	if err != nil {
		return false, &OpError{Kind: kindFor(err, KindAppend), Op: "search", Folder: folder, Err: err}
	}
	return has, nil
}

func (s *Session) loadIndexLocked(ctx context.Context, st *folderState, folder string) error {
	c, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer s.release(c)
	limit := s.opts.IndexMaxIDs
	if left := s.opts.IndexMaxTotal - int(s.indexed.Load()); left < limit {
		limit = left
	}
	if limit <= 0 {
		st.overflow = true // the session's memory budget is used up
		return nil
	}
	idx, err := c.LoadIndex(ctx, folder, limit)
	if err != nil {
		return &OpError{Kind: kindFor(err, KindAppend), Op: "read folder", Folder: folder, Err: err}
	}
	if idx.Overflow {
		st.overflow = true
		return nil
	}
	s.indexed.Add(int64(len(idx.IDs)))
	st.idx = &idx
	return nil
}

// noteDelivered records a delivered copy in the folder's index. A changed
// UIDVALIDITY means the folder was recreated behind our back: the index no
// longer describes it, so it is dropped and reloaded on next use.
func (s *Session) noteDelivered(st *folderState, id string, uidValidity uint32) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.idx == nil {
		return
	}
	if uidValidity != 0 && st.idx.UIDValidity != 0 && uidValidity != st.idx.UIDValidity {
		s.indexed.Add(-int64(len(st.idx.IDs)))
		st.idx = nil
		return
	}
	if _, dup := st.idx.IDs[id]; !dup {
		st.idx.IDs[id] = struct{}{}
		s.indexed.Add(1)
	}
}

// ensureFolder makes sure the folder exists, creating it (with parents if the
// server needs them) on first use in this session. When the server refuses
// the name, sanitised variants are tried (see nameCandidates) and the one that
// works is used for this folder from then on.
func (s *Session) ensureFolder(ctx context.Context, st *folderState, folder string) (bool, error) {
	st.mu.Lock()
	ready := st.ready
	st.mu.Unlock()
	if ready {
		return false, nil
	}

	candidates := nameCandidates(folder, s.delim)
	chosen, known := "", false
	s.mu.Lock()
	for _, c := range candidates { // a variant created earlier (this or a previous run) wins
		if s.known[normalise(c)] {
			chosen, known = c, true
			break
		}
	}
	s.mu.Unlock()

	created := false
	if !known {
		c, err := s.acquire(ctx)
		if err != nil {
			return false, err
		}
		var lastErr error
		for _, cand := range candidates {
			var made bool
			made, lastErr = s.create(ctx, c, cand)
			if lastErr == nil {
				chosen, created = cand, made
				break
			}
			if !nameRejected(lastErr) { // a different problem: another spelling will not help
				break
			}
		}
		s.release(c)
		if chosen == "" {
			return false, &OpError{Kind: kindFor(lastErr, KindFolder), Op: "create", Folder: folder, Err: lastErr}
		}
		s.mu.Lock()
		s.known[normalise(chosen)] = true
		if chosen != folder {
			s.renamed[folder] = chosen
		}
		s.mu.Unlock()
		if created {
			s.created.Add(1)
		}
	}
	st.mu.Lock()
	st.ready, st.actual = true, chosen
	st.mu.Unlock()
	return created, nil
}

// Renames lists folders the server refused under their requested name, with
// the name actually used. Names are deterministic, so later sessions find the
// same folders.
func (s *Session) Renames() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.renamed))
	for k, v := range s.renamed {
		out[k] = v
	}
	return out
}

// nameCandidates returns the spellings to try for a folder, most faithful
// first: the name itself, then with "." replaced (Maildir++ stores hierarchy
// with dots and refuses them in names), then with all punctuation replaced.
// Hierarchy delimiters are kept.
func nameCandidates(folder, delim string) []string {
	out := []string{folder}
	add := func(c string) {
		if c != "" && c != out[len(out)-1] && !containsStr(out, c) {
			out = append(out, c)
		}
	}
	add(mapRunes(folder, delim, func(r rune) bool { return r == '.' }))
	add(mapRunes(folder, delim, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '_' || r == '-')
	}))
	return out
}

// mapRunes replaces every rune matching bad by "_", except the delimiter.
func mapRunes(s, delim string, bad func(rune) bool) string {
	var b strings.Builder
	for _, r := range s {
		if string(r) != delim && bad(r) {
			b.WriteRune('_')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// nameRejected reports whether CREATE failed because of the name itself.
func nameRejected(err error) bool {
	var ie *imap.Error
	if !errors.As(err, &ie) || ie.Type != imap.StatusResponseTypeNo {
		return false
	}
	if ie.Code == imap.ResponseCodeCannot || ie.Code == imap.ResponseCodeBadCharset {
		return true
	}
	t := strings.ToLower(ie.Text)
	return strings.Contains(t, "not allowed") || strings.Contains(t, "invalid") || strings.Contains(t, "illegal")
}

// create tries the full name first (Dovecot creates parents itself) and falls
// back to creating each level for servers that do not.
func (s *Session) create(ctx context.Context, c conn, folder string) (bool, error) {
	created, err := c.CreateFolder(ctx, folder, s.opts.Subscribe)
	if err == nil || s.delim == "" || !strings.Contains(folder, s.delim) || isConnectionError(err) {
		return created, err
	}
	parts := strings.Split(folder, s.delim)
	for i := 1; i <= len(parts); i++ {
		made, perr := c.CreateFolder(ctx, strings.Join(parts[:i], s.delim), s.opts.Subscribe)
		if perr != nil {
			return false, perr
		}
		if i == len(parts) {
			created = made
		}
	}
	return created, nil
}

func (s *Session) state(folder string) *folderState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.folders[folder]
	if st == nil {
		st = &folderState{}
		s.folders[folder] = st
	}
	return st
}

// acquire returns a connection, dialling a new one when none is idle.
func (s *Session) acquire(ctx context.Context) (conn, error) {
	if err := ctx.Err(); err != nil { // select alone would pick a ready slot at random
		return nil, err
	}
	select {
	case s.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	for {
		s.mu.Lock()
		var c conn
		if n := len(s.idle); n > 0 {
			c, s.idle = s.idle[n-1], s.idle[:n-1]
		}
		s.mu.Unlock()
		if c == nil {
			break
		}
		if !c.Broken() {
			return c, nil
		}
		_ = c.Close()
	}
	c, err := s.opts.Dial(ctx)
	if err != nil {
		<-s.slots
		return nil, err
	}
	return c, nil
}

func (s *Session) release(c conn) {
	if c.Broken() {
		_ = c.Close()
	} else {
		s.mu.Lock()
		s.idle = append(s.idle, c)
		s.mu.Unlock()
	}
	<-s.slots
}

// normalise gives folder names a comparison key: IMAP treats INBOX
// case-insensitively, everything else is case sensitive.
func normalise(folder string) string { return normaliseName(folder) }

func normaliseName(folder string) string {
	if strings.EqualFold(folder, "INBOX") {
		return "INBOX"
	}
	if len(folder) > 6 && strings.EqualFold(folder[:6], "INBOX/") {
		return "INBOX" + folder[5:]
	}
	return folder
}

// kindFor keeps an existing OpError kind, otherwise classifies by cause.
func kindFor(err error, fallback Kind) Kind {
	var oe *OpError
	if errors.As(err, &oe) {
		return oe.Kind
	}
	if isConnectionError(err) {
		return KindConnection
	}
	return fallback
}

// CheckConnection verifies that the destination accepts the credentials and
// lists its folders. Used by "test connection" in the UI.
func CheckConnection(ctx context.Context, cfg Config) (folders []string, delimiter string, err error) {
	c, err := Dial(ctx, cfg)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = c.Close() }()
	if delimiter, err = c.Delimiter(ctx); err != nil {
		return nil, "", fmt.Errorf("delimiter: %w", err)
	}
	if folders, err = c.ListFolders(ctx); err != nil {
		return nil, "", fmt.Errorf("list folders: %w", err)
	}
	return folders, delimiter, nil
}
