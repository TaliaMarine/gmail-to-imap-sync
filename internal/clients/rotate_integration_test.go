//go:build integration

package clients_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/clients"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/crypto"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/testenv"
)

func TestRotateKeysReEncryptsEverythingAndIsIdempotent(t *testing.T) {
	d := testenv.DB(t)
	ctx := context.Background()
	oldSpec, _ := crypto.GenerateRingSpec("old")
	oldRing, _ := crypto.ParseRing(oldSpec)
	oldF := &clients.Factory{DB: d, Ring: oldRing, BaseURL: "https://x"}

	secret, _ := oldF.SealOAuthSecret("client-secret")
	hook, _ := oldF.SealWebhook("https://hook.example/token")
	cur, _ := d.GetAppSettings(ctx)
	if _, err := d.UpdateAppSettings(ctx, store.UpdateAppSettingsParams{OauthClientID: "id", EncOauthClientSecret: secret,
		SessionRetentionDays: cur.SessionRetentionDays, EventRetentionDays: cur.EventRetentionDays, EncNotificationWebhook: hook}); err != nil {
		t.Fatal(err)
	}
	destID, acctID := uuid.New(), uuid.New()
	pw, _ := oldF.SealPassword(destID, "imap-pass")
	if _, err := d.CreateDestination(ctx, store.CreateDestinationParams{ID: destID, Name: "d", Host: "h", Port: 993, TlsMode: "implicit", Username: "u", EncPassword: pw, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateAccount(ctx, store.CreateAccountParams{ID: acctID, Name: "a", DestinationID: uuid.NullUUID{UUID: destID, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	tok, _ := oldF.SealRefreshToken(acctID, "refresh")
	if _, err := d.SetAccountAuthorised(ctx, store.SetAccountAuthorisedParams{ID: acctID, EncRefreshToken: tok, GmailAddress: "a@gmail.com"}); err != nil {
		t.Fatal(err)
	}

	// New key first, old key kept for decryption.
	newSpec, _ := crypto.GenerateRingSpec("new")
	ring, err := crypto.ParseRing(newSpec + "," + oldSpec)
	if err != nil {
		t.Fatal(err)
	}
	f := &clients.Factory{DB: d, Ring: ring, BaseURL: "https://x"}
	st, err := f.RotateKeys(ctx)
	if err != nil || st.Rotated != 4 {
		t.Fatalf("rotate = %+v, %v", st, err)
	}
	st, err = f.RotateKeys(ctx)
	if err != nil || st.Rotated != 0 || st.Current != 4 {
		t.Fatalf("second rotate = %+v, %v", st, err)
	}

	// The old key is no longer needed: a ring with only the new key reads everything.
	onlyNew, _ := crypto.ParseRing(newSpec)
	g := &clients.Factory{DB: d, Ring: onlyNew, BaseURL: "https://x"}
	if _, err := g.OAuthConfig(ctx); err != nil {
		t.Errorf("oauth client secret: %v", err)
	}
	dst, _ := d.GetDestination(ctx, destID)
	if cfg, err := g.IMAPConfig(dst); err != nil || cfg.Password != "imap-pass" {
		t.Errorf("imap password: %q, %v", cfg.Password, err)
	}
	acct, _ := d.GetAccount(ctx, acctID)
	if acct.GmailAddress != "a@gmail.com" || acct.Status != "ok" {
		t.Errorf("account changed: %+v", acct)
	}
	s, _ := d.GetAppSettings(ctx)
	if url, err := g.OpenWebhook(s.EncNotificationWebhook); err != nil || url != "https://hook.example/token" {
		t.Errorf("webhook: %q, %v", url, err)
	}
	// A key missing from the ring aborts instead of corrupting data.
	other, _ := crypto.GenerateRingSpec("other")
	otherRing, _ := crypto.ParseRing(other)
	h := &clients.Factory{DB: d, Ring: otherRing}
	if _, err := h.RotateKeys(ctx); err == nil {
		t.Error("rotation with an unknown key succeeded")
	}
}
