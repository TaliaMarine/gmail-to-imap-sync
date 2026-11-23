package gmail

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/googleapi"
)

// Sentinel errors callers branch on (errors.Is).
var (
	// ErrAuth means the credentials are no longer valid (revoked, expired,
	// invalid_grant). Retrying cannot help; the account needs reauthorising.
	ErrAuth = errors.New("gmail: authorisation invalid")
	// ErrRateLimited means Gmail kept rate limiting after all retries.
	ErrRateLimited = errors.New("gmail: rate limited")
	// ErrMessageNotFound means the message no longer exists.
	ErrMessageNotFound = errors.New("gmail: message not found")
	// ErrHistoryExpired means the start history ID is too old (HTTP 404 from
	// history.list); the caller must reconcile instead.
	ErrHistoryExpired = errors.New("gmail: history id expired")
	// ErrMessageTooLarge means the raw message exceeds the configured limit.
	ErrMessageTooLarge = errors.New("gmail: message too large")
)

// Kind classifies an error for the failure table (FULL_SPECS.md §7).
type Kind string

const (
	KindRateLimit   Kind = "gmail_rate_limit"
	KindAuth        Kind = "gmail_auth"
	KindFetch       Kind = "gmail_fetch"
	KindUnavailable Kind = "gmail_message_unavailable"
)

// Classify maps err to a failure kind.
func Classify(err error) Kind {
	switch {
	case errors.Is(err, ErrAuth):
		return KindAuth
	case errors.Is(err, ErrRateLimited):
		return KindRateLimit
	case errors.Is(err, ErrMessageNotFound), errors.Is(err, ErrMessageTooLarge):
		return KindUnavailable
	}
	return KindFetch
}

// verdict says how the client reacts to one failed attempt.
type verdict struct {
	retry      bool
	slow       bool          // also reduce the request rate
	retryAfter time.Duration // server-provided minimum wait
	sentinel   error         // wraps the returned error when giving up (nil: none)
}

// judge inspects an error from a single attempt.
func judge(err error) verdict {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return verdict{}
	}

	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		// A token refresh failed. invalid_grant: revoked or expired for good.
		// 5xx or a network blip from the token endpoint is transient.
		if re.Response != nil && re.Response.StatusCode >= 500 {
			return verdict{retry: true}
		}
		return verdict{sentinel: ErrAuth}
	}

	var ge *googleapi.Error
	if errors.As(err, &ge) {
		switch {
		case ge.Code == http.StatusUnauthorized:
			return verdict{sentinel: ErrAuth}
		case ge.Code == http.StatusTooManyRequests, ge.Code == http.StatusForbidden && isRateReason(ge):
			return verdict{retry: true, slow: true, retryAfter: retryAfter(ge), sentinel: ErrRateLimited}
		case ge.Code == http.StatusForbidden:
			// Insufficient scope, API disabled, access not configured...
			return verdict{sentinel: ErrAuth}
		case ge.Code >= 500:
			return verdict{retry: true, retryAfter: retryAfter(ge)}
		}
		return verdict{}
	}

	// Transport errors: timeouts, resets, EOF. Retry; the context decides
	// when to give up.
	var ne net.Error
	if errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return verdict{retry: true}
	}
	return verdict{}
}

func isRateReason(ge *googleapi.Error) bool {
	for _, e := range ge.Errors {
		switch e.Reason {
		case "rateLimitExceeded", "userRateLimitExceeded", "dailyLimitExceeded", "quotaExceeded":
			return true
		}
	}
	return strings.Contains(strings.ToLower(ge.Message), "rate limit")
}

func retryAfter(ge *googleapi.Error) time.Duration {
	v := ge.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
