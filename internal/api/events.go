package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
)

const sseKeepAlive = 15 * time.Second

// eventsHandler streams live session updates (Server-Sent Events).
//
// It first sends a "snapshot" event with every queued or running session,
// then a "session" event whenever one changes. Reconnecting clients (with
// Last-Event-ID or ?lastEventId=) simply get a fresh snapshot, which makes
// resuming trivial: the snapshot is the whole truth.
func (a *API) eventsHandler(c *gin.Context) {
	account := c.Query("account")
	if account != "" {
		if _, err := uuid.Parse(account); err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"title": "Unprocessable Entity", "status": 422, "detail": "invalid account id"})
			return
		}
	}
	flusher, ok := c.Writer.(http.Flusher)
	if !ok || a.hub == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"title": "Internal Server Error", "status": 500})
		return
	}
	ctx := c.Request.Context()

	// Subscribe before reading the snapshot so nothing falls between the two.
	events, cancel := a.hub.Subscribe(account)
	defer cancel()

	h := c.Writer.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // reverse proxies must not buffer the stream
	c.Status(http.StatusOK)

	var seq atomic.Int64
	send := func(event string, data any) bool {
		b, err := json.Marshal(data)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(c.Writer, "id: %d\nevent: %s\ndata: %s\n\n", seq.Add(1), event, b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	snapshot, err := a.activeSessions(ctx, account)
	if err != nil {
		a.logger.Warn("events snapshot", "error", err)
		snapshot = []SessionSummary{}
	}
	if !send("snapshot", gin.H{"sessions": snapshot}) {
		return
	}

	keepAlive := time.NewTicker(sseKeepAlive)
	defer keepAlive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-keepAlive.C:
			if _, err := fmt.Fprint(c.Writer, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case e, open := <-events:
			if !open {
				return
			}
			id, err := uuid.Parse(e.SessionID)
			if err != nil {
				continue
			}
			s, err := a.db.GetSession(ctx, id)
			if err != nil {
				continue
			}
			if !send("session", sessionSummary(s)) {
				return
			}
		}
	}
}

func (a *API) activeSessions(ctx context.Context, account string) ([]SessionSummary, error) {
	acct, err := nullUUID(account)
	if err != nil {
		return nil, err
	}
	out := []SessionSummary{}
	for _, status := range []string{"running", "queued"} {
		rows, err := a.db.ListSessions(ctx, store.ListSessionsParams{AccountID: acct, Status: text(status), Lim: 100})
		if err != nil {
			return nil, err
		}
		out = append(out, sessionSummaries(rows)...)
	}
	return out, nil
}
