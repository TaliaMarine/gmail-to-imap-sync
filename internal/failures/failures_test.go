package failures

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/gmail"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/imapdest"
)

func TestCategorise(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		cat   string
		retry bool
	}{
		{"gmail auth", fmt.Errorf("x: %w", gmail.ErrAuth), GmailAuth, false},
		{"gmail rate limit", fmt.Errorf("x: %w", gmail.ErrRateLimited), GmailRateLimit, true},
		{"gmail gone", fmt.Errorf("x: %w", gmail.ErrMessageNotFound), GmailMessageUnavailable, false},
		{"gmail too large", fmt.Errorf("x: %w", gmail.ErrMessageTooLarge), GmailMessageUnavailable, false},
		{"unclassified fetch error", errors.New("boom"), GmailFetch, true},
		{"imap auth", &imapdest.OpError{Kind: imapdest.KindAuth, Err: errors.New("no")}, IMAPAuth, false},
		{"imap connection", &imapdest.OpError{Kind: imapdest.KindConnection, Err: io.EOF}, IMAPConnection, true},
		{"raw connection error", fmt.Errorf("w: %w", io.ErrUnexpectedEOF), IMAPConnection, true},
		{"imap folder", &imapdest.OpError{Kind: imapdest.KindFolder, Err: errors.New("x")}, IMAPFolder, true},
		{"imap append quota", &imapdest.OpError{Kind: imapdest.KindAppend, Err: &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeOverQuota}}, IMAPAppend, true},
		{"imap append no permission", &imapdest.OpError{Kind: imapdest.KindAppend, Err: &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeNoPerm}}, IMAPAppend, false},
		{"malformed", &imapdest.OpError{Kind: imapdest.KindParse, Err: imapdest.ErrMalformed}, Parse, false},
	}
	for _, tc := range cases {
		cat, retry := Categorise(tc.err)
		if cat != tc.cat || retry != tc.retry {
			t.Errorf("%s: got %s/%v, want %s/%v", tc.name, cat, retry, tc.cat, tc.retry)
		}
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	prev := time.Duration(0)
	for attempts := 1; attempts <= 4; attempts++ {
		lo, hi := time.Duration(float64(5*time.Minute<<(attempts-1))*0.8), time.Duration(float64(5*time.Minute<<(attempts-1))*1.2)
		for i := 0; i < 50; i++ {
			if d := Backoff(attempts); d < lo || d > hi {
				t.Fatalf("attempt %d: %v outside [%v, %v]", attempts, d, lo, hi)
			}
		}
		_ = prev
	}
	if d := Backoff(50); d > 24*time.Hour*12/10 || d < 24*time.Hour*8/10 {
		t.Errorf("cap: %v", d)
	}
	if Backoff(0) <= 0 {
		t.Error("attempts below 1 must still back off")
	}
}

func TestSchedule(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if next, ex := Schedule(now, 1, 10, true); !next.After(now) || ex {
		t.Errorf("retryable: %v %v", next, ex)
	}
	if next, ex := Schedule(now, 10, 10, true); !next.IsZero() || !ex {
		t.Errorf("exhausted: %v %v", next, ex)
	}
	if next, ex := Schedule(now, 1, 10, false); !next.IsZero() || ex {
		t.Errorf("permanent: %v %v", next, ex)
	}
	if _, ex := Schedule(now, 1, 0, true); !ex {
		t.Error("maxAttempts 0 disables automatic retries")
	}
}

func TestMergeHistoryNewestFirstAndCapped(t *testing.T) {
	var h []byte
	for i := 0; i < 15; i++ {
		h = MergeHistory(h, HistoryEntry{At: time.Unix(int64(i), 0), Category: Unknown, Message: fmt.Sprint(i)})
	}
	var got []HistoryEntry
	if err := json.Unmarshal(h, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxHistory || got[0].Message != "14" || got[MaxHistory-1].Message != "5" {
		t.Errorf("history = %+v", got)
	}
	if out := MergeHistory([]byte("not json"), HistoryEntry{Message: "x"}); !strings.Contains(string(out), `"x"`) {
		t.Errorf("corrupt history not replaced: %s", out)
	}
}

func TestExtractMeta(t *testing.T) {
	raw := []byte("From: =?utf-8?q?Jos=C3=A9?= <jose@example.com>\r\nTo: a@x.com, \"B\" <b@x.com>\r\nCc: c@x.com\r\n" +
		"Subject: =?utf-8?q?caf=C3=A9_menu?=\r\nDate: Mon, 05 Feb 2024 10:00:00 +0100\r\n\r\nSubject: not this\r\nbody")
	m := ExtractMeta(raw)
	if m.Subject != "café menu" {
		t.Errorf("subject = %q", m.Subject)
	}
	if !strings.Contains(m.Sender, "jose@example.com") {
		t.Errorf("sender = %q", m.Sender)
	}
	if strings.Join(m.Recipients, ",") != "a@x.com,b@x.com,c@x.com" {
		t.Errorf("recipients = %v", m.Recipients)
	}
	if !m.Date.Equal(time.Date(2024, 2, 5, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("date = %v", m.Date)
	}
}

func TestExtractMetaIsTolerant(t *testing.T) {
	for _, raw := range []string{"", "garbage", "\r\n\r\nbody", "From: <<<\r\nSubject:\r\n\r\n", strings.Repeat("X-A: b\r\n", 100000)} {
		_ = ExtractMeta([]byte(raw)) // must not panic
	}
	m := ExtractMeta([]byte("Subject: " + strings.Repeat("é", 2000) + "\r\n\r\n"))
	if len([]rune(m.Subject)) > 501 {
		t.Errorf("subject not truncated: %d", len([]rune(m.Subject)))
	}
	var many strings.Builder
	many.WriteString("To: ")
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&many, "u%d@x.com, ", i)
	}
	many.WriteString("last@x.com\r\n\r\n")
	if got := len(ExtractMeta([]byte(many.String())).Recipients); got != maxRecipients {
		t.Errorf("recipients = %d", got)
	}
}
