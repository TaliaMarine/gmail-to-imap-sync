package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/clients"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/engine"
)

// Notifier posts JSON to the configured webhook when something needs a
// person: an account must be reauthorised, or sessions keep failing. The URL
// is optional and write-only in the UI; without it nothing is sent.
//
// The payload is generic (ntfy, Slack and Discord incoming webhooks accept
// the "text"/"message" fields); it never contains credentials or message data.
type Notifier struct {
	DB      *db.DB
	Clients *clients.Factory
	HTTP    *http.Client
	Logger  *slog.Logger
	// FailureStreak is how many consecutive failed sessions raise an alert.
	FailureStreak int
}

// payload is the webhook body.
type payload struct {
	Event   string `json:"event"`
	Account string `json:"account"`
	Text    string `json:"text"`
	Message string `json:"message"` // same as text, for ntfy-style receivers
}

// SessionFinished inspects a finished session and sends alerts as needed.
func (n *Notifier) SessionFinished(ctx context.Context, s store.SyncSession, authFailed bool) {
	acct, err := n.DB.GetAccount(ctx, s.AccountID)
	if err != nil {
		return
	}
	switch {
	case authFailed:
		n.send(ctx, payload{Event: "needs_reauth", Account: acct.Name,
			Text: fmt.Sprintf("Gmail Archive Sync: Google access for %q was revoked or expired. Reauthorise the account to resume syncing.", acct.Name)})
	case s.Status == engine.StatusFailed:
		streak := n.FailureStreak
		if streak <= 0 {
			streak = 3
		}
		statuses, err := n.DB.RecentSessionStatuses(ctx, store.RecentSessionStatusesParams{AccountID: s.AccountID, Limit: int32(streak + 1)})
		if err != nil || len(statuses) < streak {
			return
		}
		// Alert once, when the streak is reached: the session before the
		// streak must not itself be part of it.
		for i := 0; i < streak; i++ {
			if statuses[i] != engine.StatusFailed {
				return
			}
		}
		if len(statuses) > streak && statuses[streak] == engine.StatusFailed {
			return
		}
		n.send(ctx, payload{Event: "sessions_failing", Account: acct.Name,
			Text: fmt.Sprintf("Gmail Archive Sync: the last %d sync sessions for %q failed. Last error: %s", streak, acct.Name, s.LastError)})
	}
}

func (n *Notifier) send(ctx context.Context, p payload) {
	settings, err := n.DB.GetAppSettings(ctx)
	if err != nil || settings.EncNotificationWebhook == "" {
		return
	}
	url, err := n.Clients.OpenWebhook(settings.EncNotificationWebhook)
	if err != nil || url == "" {
		n.Logger.Warn("notification webhook cannot be decrypted")
		return
	}
	p.Message = p.Text
	body, _ := json.Marshal(p)
	client := n.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		n.Logger.Warn("notification webhook URL is invalid")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		n.Logger.Warn("notification webhook failed", "event", p.Event, "error", redactURL(err.Error(), url))
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		n.Logger.Warn("notification webhook rejected", "event", p.Event, "status", resp.StatusCode)
	}
}

// redactURL removes the webhook URL (it may carry a secret token) from text.
func redactURL(s, url string) string {
	return string(bytes.ReplaceAll([]byte(s), []byte(url), []byte("<webhook>")))
}
