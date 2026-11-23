// Package progress carries live synchronisation updates from the worker to
// every web pod through PostgreSQL LISTEN/NOTIFY, so progress reaches the UI
// whether the worker runs in the same process or in another pod.
//
// A notification only says that a session changed; subscribers read the
// session row for the numbers. The payload therefore stays tiny (the NOTIFY
// limit is 8000 bytes) and a missed notification costs nothing: the next one
// or a snapshot catches up.
package progress

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Channel is the PostgreSQL notification channel.
const Channel = "sync_progress"

// Kinds of event.
const (
	KindQueued   = "queued"
	KindProgress = "progress"
	KindFinished = "finished"
)

// Event announces a change of one session.
type Event struct {
	Kind      string `json:"kind"`
	AccountID string `json:"account_id"`
	SessionID string `json:"session_id"`
}

// Notify publishes e. It is best effort by design: callers log and go on.
func Notify(ctx context.Context, pool *pgxpool.Pool, e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, "SELECT pg_notify($1, $2)", Channel, string(b))
	return err
}

// Hub listens on the channel and fans events out to subscribers in this
// process.
type Hub struct {
	pool   *pgxpool.Pool
	logger *slog.Logger

	mu   sync.Mutex
	subs map[*subscription]struct{}
}

type subscription struct {
	account string // empty: all accounts
	ch      chan Event
}

// NewHub returns a Hub; call Run to start listening.
func NewHub(pool *pgxpool.Pool, logger *slog.Logger) *Hub {
	return &Hub{pool: pool, logger: logger, subs: map[*subscription]struct{}{}}
}

// Subscribe returns a channel of events for accountID (empty: every account)
// and a function that ends the subscription. Slow subscribers lose events
// rather than blocking the hub; each event is only a "look again" hint.
func (h *Hub) Subscribe(accountID string) (<-chan Event, func()) {
	s := &subscription{account: accountID, ch: make(chan Event, 64)}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return s.ch, func() {
		h.mu.Lock()
		if _, ok := h.subs[s]; ok {
			delete(h.subs, s)
			close(s.ch)
		}
		h.mu.Unlock()
	}
}

// Publish delivers e to local subscribers directly (used by an in-process
// worker so the UI does not depend on the database round trip).
func (h *Hub) Publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if s.account != "" && s.account != e.AccountID {
			continue
		}
		select {
		case s.ch <- e:
		default: // slow consumer: drop
		}
	}
}

// Run listens until ctx ends, reconnecting with backoff when the connection
// is lost.
func (h *Hub) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := h.listen(ctx)
		if ctx.Err() != nil {
			return
		}
		h.logger.Warn("progress listener lost, reconnecting", "error", err, "in", backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (h *Hub) listen(ctx context.Context) error {
	conn, err := h.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN "+Channel); err != nil {
		return err
	}
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		var e Event
		if json.Unmarshal([]byte(n.Payload), &e) != nil {
			continue
		}
		h.Publish(e)
	}
}
