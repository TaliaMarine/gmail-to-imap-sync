// Package gmail is a quota-aware, retrying client for the few Gmail API
// methods the synchroniser needs. It only ever reads (gmail.readonly).
package gmail

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	gapi "google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Scope is the only OAuth scope the application requests.
const Scope = gapi.GmailReadonlyScope

// Quota unit costs of the methods used (developers.google.com/gmail/api/reference/quota).
const (
	costProfile      = 1
	costLabelsList   = 1
	costMessagesList = 5
	costMessagesGet  = 5
	costHistoryList  = 2
)

var (
	requestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gmailsync_gmail_api_requests_total",
		Help: "Gmail API requests by method and outcome.",
	}, []string{"method", "code"})
	throttleSeconds = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gmailsync_gmail_quota_throttle_seconds_total",
		Help: "Time spent waiting for Gmail quota budget.",
	})
)

// Profile is the account's mailbox profile.
type Profile struct {
	Email     string
	HistoryID uint64
	Messages  int64
}

// Label is one Gmail label.
type Label struct {
	ID     string
	Name   string
	System bool
}

// ListOptions select messages for enumeration.
type ListOptions struct {
	Query     string   // Gmail search syntax, e.g. "after:2024/01/01"
	LabelIDs  []string // a message must carry all of them
	PageToken string
	PageSize  int64 // default and maximum 500
}

// MessageRef identifies a listed message.
type MessageRef struct{ ID, ThreadID string }

// ListPage is one page of message references.
type ListPage struct {
	Messages      []MessageRef
	NextPageToken string
	Estimate      int64 // Gmail's approximate total; not reliable
}

// Message is a message fetched in raw form.
type Message struct {
	ID           string
	ThreadID     string
	LabelIDs     []string
	InternalDate time.Time // when Gmail received it; used as the IMAP INTERNALDATE
	HistoryID    uint64
	SizeEstimate int64
	Raw          []byte // exact RFC 5322 bytes
}

// LabelAdded is a label change on a message from the history feed.
type LabelAdded struct {
	MessageID string
	LabelIDs  []string // labels that were added
}

// HistoryPage is one page of changes since a history ID.
type HistoryPage struct {
	MessagesAdded []MessageRef
	LabelsAdded   []LabelAdded
	NextPageToken string
	// LastRecordID is the ID of the newest history record on this page (0 if
	// the page is empty). Everything up to it has been seen once this page is
	// handled, so it is a safe intermediate checkpoint.
	LastRecordID uint64
	// HistoryID is the mailbox's current history ID as of this response, the
	// same on every page. It is the checkpoint once the LAST page is handled.
	HistoryID uint64
}

// Source is what the synchronisation engine needs from Gmail. *Client
// implements it; tests substitute fakes.
type Source interface {
	Profile(ctx context.Context) (Profile, error)
	Labels(ctx context.Context) ([]Label, error)
	ListMessages(ctx context.Context, o ListOptions) (ListPage, error)
	GetRaw(ctx context.Context, id string) (*Message, error)
	History(ctx context.Context, startHistoryID uint64, pageToken string) (HistoryPage, error)
	// Requests returns the number of API requests issued, for session stats.
	Requests() int64
}

// Options configure a Client.
type Options struct {
	// HTTPClient performs requests; it must carry the OAuth credentials
	// (see NewHTTPClient). Required.
	HTTPClient *http.Client
	// Endpoint overrides the API base URL (tests).
	Endpoint string
	// Budget is shared by all requests of the account; default 60 % of quota.
	Budget *Budget
	// MaxAttempts per request including the first; default 6.
	MaxAttempts int
	// RateLimitAttempts is the limit when Gmail answers with a rate-limit
	// error, which clears with time: default 10 (about five minutes).
	RateLimitAttempts int
	// BaseBackoff is the first retry delay, doubled each attempt with jitter;
	// default 1s, capped at MaxBackoff (default 60s).
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	// MaxMessageBytes rejects larger raw messages; default 64 MiB.
	MaxMessageBytes int64
}

// Client implements Source against the real Gmail API.
type Client struct {
	svc        *gapi.Service
	budget     *Budget
	attempts   int
	rlAttempts int
	base       time.Duration
	maxWait    time.Duration
	maxBytes   int64
	requests   atomic.Int64
	sleep      func(ctx context.Context, d time.Duration) error
}

var _ Source = (*Client)(nil)

// NewClient builds a Client.
func NewClient(ctx context.Context, o Options) (*Client, error) {
	if o.HTTPClient == nil {
		return nil, errors.New("gmail: HTTPClient is required")
	}
	opts := []option.ClientOption{option.WithHTTPClient(o.HTTPClient)}
	if o.Endpoint != "" {
		opts = append(opts, option.WithEndpoint(o.Endpoint))
	}
	svc, err := gapi.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("gmail: new service: %w", err)
	}
	c := &Client{
		svc: svc, budget: o.Budget, attempts: o.MaxAttempts, rlAttempts: o.RateLimitAttempts,
		base: o.BaseBackoff, maxWait: o.MaxBackoff, maxBytes: o.MaxMessageBytes,
		sleep: sleepCtx,
	}
	if c.budget == nil {
		c.budget = NewBudget(0.6)
	}
	if c.attempts <= 0 {
		c.attempts = 6
	}
	if c.rlAttempts <= 0 {
		c.rlAttempts = 10
	}
	if c.base <= 0 {
		c.base = time.Second
	}
	if c.maxWait <= 0 {
		c.maxWait = time.Minute
	}
	if c.maxBytes <= 0 {
		c.maxBytes = 64 << 20
	}
	return c, nil
}

// OAuthConfig returns the Google OAuth configuration for the read-only scope.
func OAuthConfig(clientID, clientSecret, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Endpoint:     google.Endpoint,
		Scopes:       []string{Scope},
	}
}

// NewHTTPClient returns an HTTP client that authenticates with a stored
// refresh token and renews access tokens automatically.
func NewHTTPClient(ctx context.Context, cfg *oauth2.Config, refreshToken string) *http.Client {
	return oauth2.NewClient(ctx, cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken}))
}

// Requests returns the number of API requests issued so far, retries included.
func (c *Client) Requests() int64 { return c.requests.Load() }

// Pacing reports how Gmail's rate limits shaped this client's pace: how often
// it was told to slow down, the total pause, the current rate and the learned
// ceiling (both as a share of Gmail's per-user quota).
func (c *Client) Pacing() (hits int, paused time.Duration, rate, ceiling float64) {
	hits, paused, r, ceil := c.budget.Stats()
	return hits, paused, r / perUserUnitsPerSecond, ceil / perUserUnitsPerSecond
}

// Budget exposes the limiter (for stats and metrics).
func (c *Client) Budget() *Budget { return c.budget }

// Profile returns the mailbox profile.
func (c *Client) Profile(ctx context.Context) (Profile, error) {
	var p *gapi.Profile
	err := c.do(ctx, "users.getProfile", costProfile, func() (err error) {
		p, err = c.svc.Users.GetProfile("me").Context(ctx).Do()
		return
	})
	if err != nil {
		return Profile{}, err
	}
	return Profile{Email: p.EmailAddress, HistoryID: p.HistoryId, Messages: p.MessagesTotal}, nil
}

// Labels lists all labels.
func (c *Client) Labels(ctx context.Context) ([]Label, error) {
	var res *gapi.ListLabelsResponse
	err := c.do(ctx, "users.labels.list", costLabelsList, func() (err error) {
		res, err = c.svc.Users.Labels.List("me").Context(ctx).Do()
		return
	})
	if err != nil {
		return nil, err
	}
	out := make([]Label, 0, len(res.Labels))
	for _, l := range res.Labels {
		out = append(out, Label{ID: l.Id, Name: l.Name, System: l.Type == "system"})
	}
	return out, nil
}

// ListMessages returns one page of message references. Spam and trash are
// never included.
func (c *Client) ListMessages(ctx context.Context, o ListOptions) (ListPage, error) {
	size := o.PageSize
	if size <= 0 || size > 500 {
		size = 500
	}
	var res *gapi.ListMessagesResponse
	err := c.do(ctx, "users.messages.list", costMessagesList, func() (err error) {
		call := c.svc.Users.Messages.List("me").MaxResults(size).Context(ctx)
		if o.Query != "" {
			call = call.Q(o.Query)
		}
		if len(o.LabelIDs) > 0 {
			call = call.LabelIds(o.LabelIDs...)
		}
		if o.PageToken != "" {
			call = call.PageToken(o.PageToken)
		}
		res, err = call.Do()
		return
	})
	if err != nil {
		return ListPage{}, err
	}
	page := ListPage{NextPageToken: res.NextPageToken, Estimate: res.ResultSizeEstimate}
	for _, m := range res.Messages {
		page.Messages = append(page.Messages, MessageRef{ID: m.Id, ThreadID: m.ThreadId})
	}
	return page, nil
}

// GetRaw fetches a message in raw RFC 5322 form.
func (c *Client) GetRaw(ctx context.Context, id string) (*Message, error) {
	var m *gapi.Message
	err := c.do(ctx, "users.messages.get", costMessagesGet, func() (err error) {
		m, err = c.svc.Users.Messages.Get("me", id).Format("raw").Context(ctx).Do()
		return
	})
	if err != nil {
		var ge *googleapi.Error
		if errors.As(err, &ge) && ge.Code == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %s", ErrMessageNotFound, id)
		}
		return nil, err
	}
	if int64(len(m.Raw)) > c.maxBytes*4/3+4 { // cheap check before decoding
		return nil, fmt.Errorf("%w: %s is about %d bytes", ErrMessageTooLarge, id, len(m.Raw)*3/4)
	}
	raw, err := decodeRaw(m.Raw)
	if err != nil {
		return nil, fmt.Errorf("gmail: decode message %s: %w", id, err)
	}
	if int64(len(raw)) > c.maxBytes {
		return nil, fmt.Errorf("%w: %s is %d bytes", ErrMessageTooLarge, id, len(raw))
	}
	return &Message{
		ID: m.Id, ThreadID: m.ThreadId, LabelIDs: m.LabelIds,
		InternalDate: time.UnixMilli(m.InternalDate).UTC(),
		HistoryID:    m.HistoryId, SizeEstimate: m.SizeEstimate, Raw: raw,
	}, nil
}

// History returns changes since startHistoryID (only message additions and
// label additions: deletions and label removals are deliberately ignored).
// It returns ErrHistoryExpired when Gmail no longer has that history.
func (c *Client) History(ctx context.Context, startHistoryID uint64, pageToken string) (HistoryPage, error) {
	var res *gapi.ListHistoryResponse
	err := c.do(ctx, "users.history.list", costHistoryList, func() (err error) {
		call := c.svc.Users.History.List("me").
			StartHistoryId(startHistoryID).
			HistoryTypes("messageAdded", "labelAdded").
			MaxResults(500).Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		res, err = call.Do()
		return
	})
	if err != nil {
		var ge *googleapi.Error
		if errors.As(err, &ge) && ge.Code == http.StatusNotFound {
			return HistoryPage{}, fmt.Errorf("%w (start %d)", ErrHistoryExpired, startHistoryID)
		}
		return HistoryPage{}, err
	}
	page := HistoryPage{NextPageToken: res.NextPageToken, HistoryID: res.HistoryId}
	for _, h := range res.History {
		if h.Id > page.LastRecordID {
			page.LastRecordID = h.Id
		}
		for _, a := range h.MessagesAdded {
			if a.Message != nil {
				page.MessagesAdded = append(page.MessagesAdded, MessageRef{ID: a.Message.Id, ThreadID: a.Message.ThreadId})
			}
		}
		for _, a := range h.LabelsAdded {
			if a.Message != nil {
				page.LabelsAdded = append(page.LabelsAdded, LabelAdded{MessageID: a.Message.Id, LabelIDs: a.LabelIds})
			}
		}
	}
	return page, nil
}

// do runs one API call under the quota budget, retrying transient failures
// with exponential backoff and jitter. Every attempt counts as a request and
// spends its quota units.
func (c *Client) do(ctx context.Context, method string, cost int, call func() error) error {
	for attempt := 1; ; attempt++ {
		before := c.budget.Throttled()
		if err := c.budget.Wait(ctx, cost); err != nil {
			return err
		}
		if d := c.budget.Throttled() - before; d > 0 {
			throttleSeconds.Add(d.Seconds())
		}

		c.requests.Add(1)
		err := call()
		requestsTotal.WithLabelValues(method, codeOf(err)).Inc()
		if err == nil {
			return nil
		}

		v := judge(err)
		limit := c.attempts
		if v.slow {
			c.budget.Slow()
			// Everyone waits, not just this request: Gmail's quotas are per
			// minute, so the account needs a real pause to recover.
			c.budget.Pause(maxDuration(v.retryAfter, 20*time.Second))
			limit = maxInt(c.attempts, c.rlAttempts)
		}
		if !v.retry || attempt >= limit {
			if v.sentinel != nil {
				return fmt.Errorf("%s: %w: %w", method, v.sentinel, err)
			}
			return fmt.Errorf("%s: %w", method, err)
		}
		if err := c.sleep(ctx, c.backoff(attempt, v.retryAfter)); err != nil {
			return err
		}
	}
}

// backoff returns base*2^(attempt-1) with ±25 % jitter, never less than the
// server's Retry-After, capped at maxWait.
func (c *Client) backoff(attempt int, retryAfter time.Duration) time.Duration {
	d := c.base << (attempt - 1)
	if d <= 0 || d > c.maxWait {
		d = c.maxWait
	}
	d = time.Duration(float64(d) * (0.75 + rand.Float64()*0.5))
	if retryAfter > d {
		d = retryAfter
	}
	return d
}

func codeOf(err error) string {
	if err == nil {
		return "200"
	}
	var ge *googleapi.Error
	if errors.As(err, &ge) {
		return strconv.Itoa(ge.Code)
	}
	return "error"
}

// decodeRaw decodes Gmail's base64url body, padded or not.
func decodeRaw(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
