package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/clients"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/gmail"
)

const (
	googleStateCookie = "gas_google_state"
	googleStateTTL    = 10 * time.Minute
)

// googleState is sealed into a short-lived cookie while the user is at Google.
type googleState struct {
	State     string    `json:"s"`
	Verifier  string    `json:"v"`
	AccountID uuid.UUID `json:"a"`
	Expires   int64     `json:"e"`
}

type OAuthStartOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      struct {
		URL string `json:"url" doc:"Send the browser here to authorise the account with Google"`
	}
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (a *API) startOAuth(ctx context.Context, in *IDInput) (*OAuthStartOutput, error) {
	acct, err := a.loadAccount(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	cfg, err := a.clients.OAuthConfig(ctx)
	if errors.Is(err, clients.ErrNotConfigured) {
		return nil, huma.Error409Conflict("Google sign-in is not set up yet. Add your OAuth client in settings first.")
	}
	if err != nil {
		return nil, a.internalError(err, "load oauth config")
	}
	state, err1 := randomToken(24)
	if err1 != nil {
		return nil, a.internalError(err1, "random state")
	}
	verifier := oauth2.GenerateVerifier()
	sealed, err := a.stateSealer.Seal(googleState{
		State: state, Verifier: verifier, AccountID: acct.ID, Expires: time.Now().Add(googleStateTTL).Unix(),
	})
	if err != nil {
		return nil, a.internalError(err, "seal oauth state")
	}
	out := &OAuthStartOutput{}
	out.SetCookie = a.stateCookie(sealed, int(googleStateTTL.Seconds()))
	out.Body.URL = cfg.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"), // always return a refresh token
		oauth2.SetAuthURLParam("login_hint", acct.GmailAddress),
		oauth2.S256ChallengeOption(verifier),
	)
	return out, nil
}

func (a *API) stateCookie(value string, maxAge int) http.Cookie {
	return http.Cookie{
		Name: googleStateCookie, Value: value, Path: "/oauth", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: a.secureCookies,
	}
}

// oauthCallback completes the Google authorisation. It is a browser
// navigation, so failures redirect back to the account page with a short
// error code instead of returning JSON.
func (a *API) oauthCallback(c *gin.Context) {
	fail := func(acct uuid.UUID, code string) {
		target := "/accounts"
		if acct != uuid.Nil {
			target += "/" + acct.String()
		}
		c.Redirect(http.StatusFound, target+"?oauth_error="+url.QueryEscape(code))
	}
	clear := a.stateCookie("", -1)
	http.SetCookie(c.Writer, &clear) // single use, whatever happens next

	cookie, err := c.Request.Cookie(googleStateCookie)
	if err != nil {
		fail(uuid.Nil, "missing_state")
		return
	}
	var st googleState
	if err := a.stateSealer.Open(cookie.Value, &st); err != nil || time.Now().Unix() > st.Expires {
		fail(uuid.Nil, "expired_state")
		return
	}
	if subtle.ConstantTimeCompare([]byte(c.Query("state")), []byte(st.State)) != 1 {
		fail(st.AccountID, "state_mismatch")
		return
	}
	if e := c.Query("error"); e != "" {
		code := "denied"
		if e != "access_denied" {
			code = "google_error"
		}
		fail(st.AccountID, code)
		return
	}
	ctx := c.Request.Context()
	acct, err := a.db.GetAccount(ctx, st.AccountID)
	if err != nil {
		fail(uuid.Nil, "unknown_account")
		return
	}
	cfg, err := a.clients.OAuthConfig(ctx)
	if err != nil {
		fail(acct.ID, "not_configured")
		return
	}
	hctx := ctx
	if a.clients.HTTPClient != nil {
		hctx = context.WithValue(ctx, oauth2.HTTPClient, a.clients.HTTPClient)
	}
	token, err := cfg.Exchange(hctx, c.Query("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		a.logger.Warn("google token exchange failed", "account", acct.ID)
		fail(acct.ID, "exchange_failed")
		return
	}
	if token.RefreshToken == "" {
		// Google only omits it when the grant was not new; prompt=consent normally prevents that.
		fail(acct.ID, "no_refresh_token")
		return
	}
	hc := oauth2.NewClient(hctx, cfg.TokenSource(hctx, token))
	g, err := gmail.NewClient(ctx, gmail.Options{HTTPClient: hc, Endpoint: a.clients.GmailEndpoint})
	var profile gmail.Profile
	if err == nil {
		pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		profile, err = g.Profile(pctx)
		cancel()
	}
	if err != nil {
		a.logger.Warn("gmail profile after authorisation failed", "account", acct.ID, "error", err)
		fail(acct.ID, "profile_failed")
		return
	}
	enc, err := a.clients.SealRefreshToken(acct.ID, token.RefreshToken)
	if err != nil {
		a.logger.Error("encrypt refresh token", "error", err)
		fail(acct.ID, "internal")
		return
	}
	if acct.GmailAddress != "" && !strings.EqualFold(acct.GmailAddress, profile.Email) {
		// A different mailbox: its history IDs and everything recorded about
		// the old one mean nothing here. Start from a clean sync state.
		a.logger.Warn("account reauthorised with a different Google account, resetting sync state", "account", acct.ID)
		if err := a.db.ResetInitialSync(ctx, acct.ID); err != nil {
			a.logger.Error("reset sync state", "error", err)
			fail(acct.ID, "internal")
			return
		}
		if err := a.db.DeleteFailuresForAccount(ctx, acct.ID); err != nil {
			a.logger.Error("clear failures", "error", err)
			fail(acct.ID, "internal")
			return
		}
	}
	if _, err := a.db.SetAccountAuthorised(ctx, store.SetAccountAuthorisedParams{
		ID: acct.ID, EncRefreshToken: enc, GmailAddress: profile.Email,
	}); err != nil {
		a.logger.Error("store refresh token", "error", err)
		fail(acct.ID, "internal")
		return
	}
	a.logger.Info("account authorised", "account", acct.ID)
	c.Redirect(http.StatusFound, "/accounts/"+acct.ID.String())
}

func (a *API) registerOAuth(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "start-account-oauth", Method: http.MethodPost, Path: "/api/v1/accounts/{id}/oauth/start",
		Summary:     "Start authorising an account with Google",
		Description: "Returns the Google consent URL. Redirect the browser there; Google returns to /oauth/callback.",
		Tags:        []string{"Accounts"}}, a.startOAuth)
}
