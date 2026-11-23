// Package clients builds Gmail and IMAP clients from what is stored in the
// database, and owns the encryption contexts of every stored secret, so the
// API (which writes secrets) and the sync engine (which reads them) agree.
package clients

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/crypto"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/gmail"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/imapdest"
)

// ErrNotConfigured means the Google OAuth client has not been set up yet.
var ErrNotConfigured = errors.New("google oauth client is not configured")

// ErrNotAuthorised means the account has no refresh token.
var ErrNotAuthorised = errors.New("account is not authorised with google")

// Factory creates clients.
type Factory struct {
	DB      *db.DB
	Ring    *crypto.Ring
	BaseURL string // public URL; forms the Google redirect URI
	// HTTPClient, when set, is used for token refresh and API calls instead of
	// the default (tests).
	HTTPClient *http.Client
	// GmailEndpoint overrides the Gmail API base URL (tests).
	GmailEndpoint string
}

// Encryption contexts bind a ciphertext to its column and row.
const (
	ctxOAuthSecret = "app_settings.oauth_client_secret"
	ctxWebhook     = "app_settings.notification_webhook"
)

func ctxRefreshToken(id uuid.UUID) string { return "accounts.refresh_token." + id.String() }
func ctxPassword(id uuid.UUID) string     { return "destinations.password." + id.String() }

// SealOAuthSecret encrypts the Google client secret.
func (f *Factory) SealOAuthSecret(secret string) (string, error) {
	return f.seal(secret, ctxOAuthSecret)
}

// SealWebhook encrypts the notification webhook URL.
func (f *Factory) SealWebhook(url string) (string, error) { return f.seal(url, ctxWebhook) }

// OpenWebhook decrypts the notification webhook URL ("" when unset).
func (f *Factory) OpenWebhook(enc string) (string, error) { return f.open(enc, ctxWebhook) }

// SealRefreshToken encrypts an account's refresh token.
func (f *Factory) SealRefreshToken(id uuid.UUID, token string) (string, error) {
	return f.seal(token, ctxRefreshToken(id))
}

// SealPassword encrypts a destination's IMAP password.
func (f *Factory) SealPassword(id uuid.UUID, password string) (string, error) {
	return f.seal(password, ctxPassword(id))
}

func (f *Factory) seal(v, c string) (string, error) {
	if v == "" {
		return "", nil
	}
	return f.Ring.Encrypt([]byte(v), c)
}

func (f *Factory) open(enc, c string) (string, error) {
	if enc == "" {
		return "", nil
	}
	b, err := f.Ring.Decrypt(enc, c)
	return string(b), err
}

// OAuthConfig returns the Google OAuth configuration from the stored
// settings.
func (f *Factory) OAuthConfig(ctx context.Context) (*oauth2.Config, error) {
	s, err := f.DB.GetAppSettings(ctx)
	if err != nil {
		return nil, err
	}
	if s.OauthClientID == "" || s.EncOauthClientSecret == "" {
		return nil, ErrNotConfigured
	}
	secret, err := f.open(s.EncOauthClientSecret, ctxOAuthSecret)
	if err != nil {
		return nil, fmt.Errorf("decrypt oauth client secret: %w", err)
	}
	return gmail.OAuthConfig(s.OauthClientID, secret, f.BaseURL+"/oauth/callback"), nil
}

// Gmail builds a Gmail source for the account using its refresh token. The
// budget (quota share) may be shared across calls for the same account.
func (f *Factory) Gmail(ctx context.Context, a store.Account, budget *gmail.Budget) (*gmail.Client, error) {
	if a.EncRefreshToken == "" {
		return nil, ErrNotAuthorised
	}
	cfg, err := f.OAuthConfig(ctx)
	if err != nil {
		return nil, err
	}
	token, err := f.open(a.EncRefreshToken, ctxRefreshToken(a.ID))
	if err != nil {
		return nil, fmt.Errorf("decrypt refresh token: %w", err)
	}
	// The token source must outlive ctx of this call (the client is used for a
	// whole session), so it is created from a background-derived context that
	// keeps only the HTTP client override.
	tokenCtx := context.Background()
	if f.HTTPClient != nil {
		tokenCtx = context.WithValue(tokenCtx, oauth2.HTTPClient, f.HTTPClient)
	}
	return gmail.NewClient(ctx, gmail.Options{
		HTTPClient: gmail.NewHTTPClient(tokenCtx, cfg, token),
		Endpoint:   f.GmailEndpoint,
		Budget:     budget,
	})
}

// IMAPConfig returns the connection settings of a destination with its
// password decrypted.
func (f *Factory) IMAPConfig(d store.Destination) (imapdest.Config, error) {
	pw, err := f.open(d.EncPassword, ctxPassword(d.ID))
	if err != nil {
		return imapdest.Config{}, fmt.Errorf("decrypt imap password: %w", err)
	}
	return imapdest.Config{
		Host: d.Host, Port: int(d.Port), TLSMode: d.TlsMode, SkipVerify: d.TlsSkipVerify,
		Username: d.Username, Password: pw,
	}, nil
}
