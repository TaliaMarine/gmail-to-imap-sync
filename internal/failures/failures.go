// Package failures classifies delivery errors and decides when a failed
// message is retried. Failed messages are the only per-message records the
// application keeps (FULL_SPECS.md §7).
package failures

import (
	"encoding/json"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/gmail"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/imapdest"
)

// Error categories stored in failed_messages.error_category.
const (
	GmailRateLimit          = "gmail_rate_limit"
	GmailAuth               = "gmail_auth"
	GmailFetch              = "gmail_fetch"
	GmailMessageUnavailable = "gmail_message_unavailable"
	IMAPAuth                = "imap_auth"
	IMAPConnection          = "imap_connection"
	IMAPFolder              = "imap_folder"
	IMAPAppend              = "imap_append"
	Parse                   = "parse"
	Config                  = "config"
	Unknown                 = "unknown"
)

// Categorise maps an error from Gmail or the destination to a category, and
// says whether retrying later can help.
func Categorise(err error) (category string, retryable bool) {
	switch {
	case err == nil:
		return Unknown, false
	case errors.Is(err, gmail.ErrAuth):
		return GmailAuth, false
	case errors.Is(err, gmail.ErrRateLimited):
		return GmailRateLimit, true
	case errors.Is(err, gmail.ErrMessageNotFound), errors.Is(err, gmail.ErrMessageTooLarge):
		return GmailMessageUnavailable, false
	}
	// Destination errors carry their own kind.
	var oe *imapdest.OpError
	if errors.As(err, &oe) || isIMAP(err) {
		kind := imapdest.KindOf(err)
		retry := imapdest.Retryable(err)
		switch kind {
		case imapdest.KindAuth:
			return IMAPAuth, false
		case imapdest.KindConnection:
			return IMAPConnection, retry
		case imapdest.KindFolder:
			return IMAPFolder, retry
		case imapdest.KindParse:
			return Parse, false
		default:
			return IMAPAppend, retry
		}
	}
	if errors.Is(err, imapdest.ErrMalformed) {
		return Parse, false
	}
	// Anything else came from fetching the message (delivery errors are always
	// wrapped by imapdest).
	return GmailFetch, true
}

func isIMAP(err error) bool { return imapdest.KindOf(err) == imapdest.KindConnection }

// Backoff returns the wait before retry number attempts (1 = first retry):
// 5 minutes doubling up to 24 hours, with ±20 % jitter.
func Backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := 5 * time.Minute
	for i := 1; i < attempts && d < 24*time.Hour; i++ {
		d *= 2
	}
	if d > 24*time.Hour {
		d = 24 * time.Hour
	}
	return time.Duration(float64(d) * (0.8 + rand.Float64()*0.4))
}

// Schedule decides what happens after a failed attempt number attempts: when
// to retry (zero time: never automatically) and whether automatic retries
// are exhausted. maxAttempts 0 disables automatic retries.
func Schedule(now time.Time, attempts, maxAttempts int, retryable bool) (next time.Time, exhausted bool) {
	if !retryable {
		return time.Time{}, false // needs a fix first; retried manually or after reconfiguring
	}
	if attempts >= maxAttempts {
		return time.Time{}, true
	}
	return now.Add(Backoff(attempts)), false
}

// HistoryEntry is one failed attempt.
type HistoryEntry struct {
	At       time.Time `json:"at"`
	Category string    `json:"category"`
	Message  string    `json:"message"`
	Session  string    `json:"session_id,omitempty"`
}

// MaxHistory is how many attempts are remembered per message.
const MaxHistory = 10

// MergeHistory returns the stored history with e added in front, newest
// first, capped at MaxHistory. A corrupt stored value is replaced.
func MergeHistory(prev []byte, e HistoryEntry) []byte {
	var hist []HistoryEntry
	_ = json.Unmarshal(prev, &hist)
	hist = append([]HistoryEntry{e}, hist...)
	if len(hist) > MaxHistory {
		hist = hist[:MaxHistory]
	}
	b, _ := json.Marshal(hist)
	return b
}

// Truncate shortens an error message for storage and display.
func Truncate(s string) string {
	const max = 500
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
