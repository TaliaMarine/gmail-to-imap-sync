package api

import (
	"context"
	"net/http"
	"net/url"

	"github.com/danielgtaylor/huma/v2"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
)

// Settings are the global, application-wide settings. Secrets are write-only.
type Settings struct {
	OAuthClientID          string `json:"oauth_client_id" doc:"Client ID of your Google OAuth client"`
	OAuthClientSecretSet   bool   `json:"oauth_client_secret_set" doc:"Whether a client secret is stored (it is never returned)"`
	RedirectURI            string `json:"redirect_uri" doc:"Register this redirect URI in the Google OAuth client"`
	Configured             bool   `json:"configured" doc:"Whether Google sign-in for accounts is set up"`
	SessionRetentionDays   int32  `json:"session_retention_days"`
	EventRetentionDays     int32  `json:"event_retention_days"`
	NotificationWebhookSet bool   `json:"notification_webhook_set" doc:"Whether a notification webhook is stored (the URL is never returned)"`
}

type SettingsUpdate struct {
	OAuthClientID string `json:"oauth_client_id" maxLength:"256"`
	// OAuthClientSecret replaces the stored secret; omit it to keep the current
	// one, send an empty string to remove it.
	OAuthClientSecret *string `json:"oauth_client_secret,omitempty" maxLength:"512"`
	// NotificationWebhook replaces the stored webhook URL; omit it to keep
	// the current one, send an empty string to remove it.
	NotificationWebhook  *string `json:"notification_webhook,omitempty" maxLength:"2048"`
	SessionRetentionDays int32   `json:"session_retention_days" minimum:"1" maximum:"3650"`
	EventRetentionDays   int32   `json:"event_retention_days" minimum:"1" maximum:"3650"`
}

type SettingsOutput struct{ Body Settings }
type SettingsInput struct{ Body SettingsUpdate }

func (a *API) settingsBody(s store.AppSetting) Settings {
	return Settings{
		OAuthClientID:          s.OauthClientID,
		OAuthClientSecretSet:   s.EncOauthClientSecret != "",
		RedirectURI:            a.cfg.BaseURL + "/oauth/callback",
		Configured:             s.OauthClientID != "" && s.EncOauthClientSecret != "",
		SessionRetentionDays:   s.SessionRetentionDays,
		EventRetentionDays:     s.EventRetentionDays,
		NotificationWebhookSet: s.EncNotificationWebhook != "",
	}
}

func (a *API) getSettings(ctx context.Context, _ *struct{}) (*SettingsOutput, error) {
	s, err := a.db.GetAppSettings(ctx)
	if err != nil {
		return nil, a.internalError(err, "load settings")
	}
	return &SettingsOutput{Body: a.settingsBody(s)}, nil
}

func (a *API) updateSettings(ctx context.Context, in *SettingsInput) (*SettingsOutput, error) {
	cur, err := a.db.GetAppSettings(ctx)
	if err != nil {
		return nil, a.internalError(err, "load settings")
	}
	p := store.UpdateAppSettingsParams{
		OauthClientID:          in.Body.OAuthClientID,
		EncOauthClientSecret:   cur.EncOauthClientSecret,
		SessionRetentionDays:   in.Body.SessionRetentionDays,
		EventRetentionDays:     in.Body.EventRetentionDays,
		EncNotificationWebhook: cur.EncNotificationWebhook,
	}
	if in.Body.OAuthClientSecret != nil {
		if p.EncOauthClientSecret, err = a.clients.SealOAuthSecret(*in.Body.OAuthClientSecret); err != nil {
			return nil, a.internalError(err, "encrypt oauth client secret")
		}
	}
	if w := in.Body.NotificationWebhook; w != nil {
		if *w != "" {
			if u, err := url.Parse(*w); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
				return nil, huma.Error422UnprocessableEntity("notification_webhook must be an http(s) URL")
			}
		}
		if p.EncNotificationWebhook, err = a.clients.SealWebhook(*w); err != nil {
			return nil, a.internalError(err, "encrypt webhook")
		}
	}
	s, err := a.db.UpdateAppSettings(ctx, p)
	if err != nil {
		return nil, a.internalError(err, "update settings")
	}
	return &SettingsOutput{Body: a.settingsBody(s)}, nil
}

func (a *API) registerSettings(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-settings", Method: http.MethodGet, Path: "/api/v1/settings",
		Summary: "Get application settings", Tags: []string{"Settings"},
	}, a.getSettings)
	huma.Register(api, huma.Operation{
		OperationID: "update-settings", Method: http.MethodPut, Path: "/api/v1/settings",
		Summary: "Update application settings", Tags: []string{"Settings"},
	}, a.updateSettings)
}
