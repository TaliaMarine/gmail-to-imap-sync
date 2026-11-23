package imapdest

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// TLS modes of a destination.
const (
	TLSImplicit = "implicit"
	TLSStartTLS = "starttls"
	TLSNone     = "none"
)

// Config describes the destination server.
type Config struct {
	Host        string
	Port        int
	TLSMode     string // implicit, starttls or none
	SkipVerify  bool   // accept any certificate; opt-in, shown as a warning in the UI
	Username    string
	Password    string
	DialTimeout time.Duration // default 15s
	OpTimeout   time.Duration // per command, default 2m
}

func (c Config) addr() string { return net.JoinHostPort(c.Host, strconv.Itoa(c.Port)) }

// conn is one destination connection. All its methods take a context: when it
// ends, the connection is closed (go-imap commands cannot be interrupted any
// other way) and marked broken.
type conn interface {
	Delimiter(ctx context.Context) (string, error)
	ListFolders(ctx context.Context) ([]string, error)
	// CreateFolder creates folder (and subscribes to it); created is false
	// when it already existed.
	CreateFolder(ctx context.Context, folder string, subscribe bool) (created bool, err error)
	// LoadIndex reads the archive IDs of every message in folder. When the
	// folder holds more than maxIDs of them, IDs is nil and Overflow is set.
	LoadIndex(ctx context.Context, folder string, maxIDs int) (Index, error)
	// HasID reports whether folder holds a message with exactly this archive ID.
	HasID(ctx context.Context, folder, value string) (bool, error)
	Append(ctx context.Context, folder string, raw []byte, flags []string, date time.Time) (AppendResult, error)
	Broken() bool
	Close() error
}

// Index is the set of archive IDs present in one folder.
type Index struct {
	UIDValidity uint32
	IDs         map[string]struct{}
	Overflow    bool // too many messages to hold in memory: use HasID instead
}

// AppendResult is what APPEND reports (needs UIDPLUS; zero otherwise).
type AppendResult struct {
	UIDValidity uint32
	UID         uint32
}

// Conn is the go-imap backed implementation of conn.
type Conn struct {
	c        *imapclient.Client
	cfg      Config
	broken   atomic.Bool
	selected string
}

var _ conn = (*Conn)(nil)

// Dial connects and logs in.
func Dial(ctx context.Context, cfg Config) (*Conn, error) {
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 15 * time.Second
	}
	if cfg.OpTimeout <= 0 {
		cfg.OpTimeout = 2 * time.Minute
	}
	opts := &imapclient.Options{
		Dialer:    &net.Dialer{Timeout: cfg.DialTimeout},
		TLSConfig: &tls.Config{ServerName: cfg.Host, InsecureSkipVerify: cfg.SkipVerify, MinVersion: tls.VersionTLS12}, //nolint:gosec // opt-in per destination
	}
	var (
		cl  *imapclient.Client
		err error
	)
	// DialTLS and friends are not context aware; run them so ctx still bounds the wait.
	type result struct {
		c   *imapclient.Client
		err error
	}
	ch := make(chan result, 1)
	go func() {
		var r result
		switch cfg.TLSMode {
		case TLSNone:
			r.c, r.err = imapclient.DialInsecure(cfg.addr(), opts)
		case TLSStartTLS:
			r.c, r.err = imapclient.DialStartTLS(cfg.addr(), opts)
		default:
			r.c, r.err = imapclient.DialTLS(cfg.addr(), opts)
		}
		ch <- r
	}()
	select {
	case r := <-ch:
		cl, err = r.c, r.err
	case <-ctx.Done():
		go func() { // do not leak a connection that completes after we gave up
			if r := <-ch; r.c != nil {
				_ = r.c.Close()
			}
		}()
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, &OpError{Kind: KindConnection, Op: "connect " + cfg.addr(), Err: err}
	}
	c := &Conn{c: cl, cfg: cfg}
	if err := c.run(ctx, func() error { return cl.Login(cfg.Username, cfg.Password).Wait() }); err != nil {
		_ = c.Close()
		kind := KindConnection
		var ie *imap.Error
		if errors.As(err, &ie) && ie.Type != imap.StatusResponseTypeBye {
			kind = KindAuth // NO/BAD to LOGIN: wrong credentials or login disabled
		}
		return nil, &OpError{Kind: kind, Op: "login", Err: err}
	}
	return c, nil
}

// run executes fn, closing the connection if ctx ends or the per-command
// timeout passes first.
func (c *Conn) run(ctx context.Context, fn func() error) error {
	if c.broken.Load() {
		return errBroken
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	opCtx, cancel := context.WithTimeout(ctx, c.cfg.OpTimeout)
	done := make(chan struct{})
	go func() {
		select {
		case <-opCtx.Done():
			c.broken.Store(true)
			_ = c.c.Close()
		case <-done:
		}
	}()
	err := fn()
	close(done)
	timedOut := opCtx.Err() != nil
	cancel()
	if err != nil && timedOut {
		if ce := ctx.Err(); ce != nil {
			return ce
		}
		return fmt.Errorf("%w: command timed out after %s", errBroken, c.cfg.OpTimeout)
	}
	if err != nil && isConnectionError(err) {
		c.broken.Store(true)
	}
	return err
}

// Broken reports whether the connection must be discarded.
func (c *Conn) Broken() bool { return c.broken.Load() }

// Close ends the connection.
func (c *Conn) Close() error {
	c.broken.Store(true)
	return c.c.Close()
}

// Delimiter returns the server's hierarchy delimiter.
func (c *Conn) Delimiter(ctx context.Context) (string, error) {
	var list []*imap.ListData
	err := c.run(ctx, func() (err error) {
		list, err = c.c.List("", "", nil).Collect()
		return
	})
	if err != nil {
		return "", err
	}
	for _, l := range list {
		if l.Delim != 0 {
			return string(l.Delim), nil
		}
	}
	return "/", nil
}

// ListFolders returns the names of all existing folders.
func (c *Conn) ListFolders(ctx context.Context) ([]string, error) {
	var list []*imap.ListData
	err := c.run(ctx, func() (err error) {
		list, err = c.c.List("", "*", nil).Collect()
		return
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list))
	for _, l := range list {
		out = append(out, l.Mailbox)
	}
	return out, nil
}

// CreateFolder creates folder, tolerating "already exists".
func (c *Conn) CreateFolder(ctx context.Context, folder string, subscribe bool) (bool, error) {
	created := true
	err := c.run(ctx, func() error { return c.c.Create(folder, nil).Wait() })
	var ie *imap.Error
	if errors.As(err, &ie) && ie.Code == imap.ResponseCodeAlreadyExists {
		created, err = false, nil
	}
	if err != nil {
		return false, err
	}
	if subscribe {
		// Failing to subscribe never blocks delivery: the folder exists.
		if err := c.run(ctx, func() error { return c.c.Subscribe(folder).Wait() }); err != nil && isConnectionError(err) {
			return created, err
		}
	}
	return created, nil
}

func (c *Conn) examine(ctx context.Context, folder string) (*imap.SelectData, error) {
	var data *imap.SelectData
	err := c.run(ctx, func() (err error) {
		data, err = c.c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
		return
	})
	if err != nil {
		c.selected = ""
		return nil, err
	}
	c.selected = folder
	return data, nil
}

const indexChunk = 5000

var headerSection = &imap.FetchItemBodySection{
	Specifier:    imap.PartSpecifierHeader,
	HeaderFields: []string{HeaderName},
	Peek:         true,
}

// LoadIndex reads the archive header of every message in folder, in chunks so
// memory stays bounded by the chunk and the resulting set.
func (c *Conn) LoadIndex(ctx context.Context, folder string, maxIDs int) (Index, error) {
	sel, err := c.examine(ctx, folder)
	if err != nil {
		return Index{}, err
	}
	idx := Index{UIDValidity: sel.UIDValidity, IDs: map[string]struct{}{}}
	for start := uint32(1); start <= sel.NumMessages; start += indexChunk {
		stop := start + indexChunk - 1
		if stop > sel.NumMessages {
			stop = sel.NumMessages
		}
		var set imap.SeqSet
		set.AddRange(start, stop)
		var msgs []*imapclient.FetchMessageBuffer
		err := c.run(ctx, func() (err error) {
			msgs, err = c.c.Fetch(set, &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{headerSection}}).Collect()
			return
		})
		if err != nil {
			return Index{}, err
		}
		for _, m := range msgs {
			for _, v := range headerValues(m.FindBodySection(headerSection)) {
				idx.IDs[v] = struct{}{}
			}
		}
		if len(idx.IDs) > maxIDs {
			return Index{UIDValidity: sel.UIDValidity, Overflow: true}, nil
		}
	}
	return idx, nil
}

// HasID searches the folder for the archive header. IMAP SEARCH HEADER is a
// substring match, so every hit is verified for an exact value.
func (c *Conn) HasID(ctx context.Context, folder, value string) (bool, error) {
	if _, err := c.examine(ctx, folder); err != nil {
		return false, err
	}
	var found *imap.SearchData
	err := c.run(ctx, func() (err error) {
		found, err = c.c.UIDSearch(&imap.SearchCriteria{
			Header: []imap.SearchCriteriaHeaderField{{Key: HeaderName, Value: value}},
		}, nil).Wait()
		return
	})
	if err != nil {
		return false, err
	}
	uids := found.AllUIDs()
	if len(uids) == 0 {
		return false, nil
	}
	var set imap.UIDSet
	set.AddNum(uids...)
	var msgs []*imapclient.FetchMessageBuffer
	err = c.run(ctx, func() (err error) {
		msgs, err = c.c.Fetch(set, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{headerSection}}).Collect()
		return
	})
	if err != nil {
		return false, err
	}
	for _, m := range msgs {
		for _, v := range headerValues(m.FindBodySection(headerSection)) {
			if v == value {
				return true, nil
			}
		}
	}
	return false, nil
}

// Append delivers raw to folder with the given flags and internal date.
func (c *Conn) Append(ctx context.Context, folder string, raw []byte, flags []string, date time.Time) (AppendResult, error) {
	opts := &imap.AppendOptions{Time: date}
	for _, f := range flags {
		opts.Flags = append(opts.Flags, imap.Flag(f))
	}
	var data *imap.AppendData
	err := c.run(ctx, func() error {
		cmd := c.c.Append(folder, int64(len(raw)), opts)
		if _, err := io.Copy(cmd, bytes.NewReader(raw)); err != nil {
			_ = cmd.Close()
			return err
		}
		if err := cmd.Close(); err != nil {
			return err
		}
		var err error
		data, err = cmd.Wait()
		return err
	})
	if err != nil {
		return AppendResult{}, err
	}
	return AppendResult{UIDValidity: data.UIDValidity, UID: uint32(data.UID)}, nil
}
