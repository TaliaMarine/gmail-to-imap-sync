package clients

import (
	"context"
	"fmt"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
)

// RotateStats reports what RotateKeys changed.
type RotateStats struct {
	Rotated int // values re-encrypted with the active key
	Current int // values already using it
}

// RotateKeys re-encrypts every stored secret with the active key of the ring,
// so an old key can be removed from ENCRYPTION_KEYS. Values that cannot be
// decrypted abort the run with an error naming the row (never the value).
// It is idempotent and safe to run while the application is serving.
func (f *Factory) RotateKeys(ctx context.Context) (RotateStats, error) {
	var st RotateStats
	re := func(enc, bind string) (string, bool, error) {
		if enc == "" {
			return "", false, nil
		}
		if !f.Ring.NeedsRotation(enc) {
			st.Current++
			return enc, false, nil
		}
		pt, err := f.Ring.Decrypt(enc, bind)
		if err != nil {
			return "", false, err
		}
		out, err := f.Ring.Encrypt(pt, bind)
		if err != nil {
			return "", false, err
		}
		st.Rotated++
		return out, true, nil
	}

	s, err := f.DB.GetAppSettings(ctx)
	if err != nil {
		return st, err
	}
	secret, c1, err := re(s.EncOauthClientSecret, ctxOAuthSecret)
	if err != nil {
		return st, fmt.Errorf("app settings oauth client secret: %w", err)
	}
	hook, c2, err := re(s.EncNotificationWebhook, ctxWebhook)
	if err != nil {
		return st, fmt.Errorf("app settings webhook: %w", err)
	}
	if c1 || c2 {
		if _, err := f.DB.UpdateAppSettings(ctx, store.UpdateAppSettingsParams{
			OauthClientID: s.OauthClientID, EncOauthClientSecret: secret,
			SessionRetentionDays: s.SessionRetentionDays, EventRetentionDays: s.EventRetentionDays,
			EncNotificationWebhook: hook}); err != nil {
			return st, err
		}
	}

	dests, err := f.DB.ListDestinations(ctx)
	if err != nil {
		return st, err
	}
	for _, d := range dests {
		pw, changed, err := re(d.EncPassword, ctxPassword(d.ID))
		if err != nil {
			return st, fmt.Errorf("destination %s password: %w", d.Name, err)
		}
		if changed {
			if _, err := f.DB.UpdateDestination(ctx, store.UpdateDestinationParams{ID: d.ID, Name: d.Name, Host: d.Host, Port: d.Port,
				TlsMode: d.TlsMode, TlsSkipVerify: d.TlsSkipVerify, Username: d.Username, EncPassword: pw, Enabled: d.Enabled}); err != nil {
				return st, err
			}
		}
	}

	accts, err := f.DB.ListAccounts(ctx)
	if err != nil {
		return st, err
	}
	for _, a := range accts {
		tok, changed, err := re(a.EncRefreshToken, ctxRefreshToken(a.ID))
		if err != nil {
			return st, fmt.Errorf("account %s refresh token: %w", a.Name, err)
		}
		if changed {
			if _, err := f.DB.SetAccountAuthorised(ctx, store.SetAccountAuthorisedParams{ID: a.ID, EncRefreshToken: tok, GmailAddress: a.GmailAddress}); err != nil {
				return st, err
			}
		}
	}
	return st, nil
}
