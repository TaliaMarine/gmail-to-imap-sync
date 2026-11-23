package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/auth"
)

type UserInfo struct {
	Email             string `json:"email" doc:"Email address"`
	Name              string `json:"name" doc:"Display name"`
	PreferredUsername string `json:"preferred_username" doc:"Preferred username"`
	CanSignOut        bool   `json:"can_sign_out" doc:"Whether the session can be ended from the UI (OIDC sign-in)"`
}

type GetMeOutput struct{ Body UserInfo }

func (a *API) getMe(ctx context.Context, _ *struct{}) (*GetMeOutput, error) {
	u, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, huma.Error401Unauthorized("authentication required")
	}
	return &GetMeOutput{Body: UserInfo{
		Email:             u.Email,
		Name:              u.Name,
		PreferredUsername: u.PreferredUsername,
		CanSignOut:        a.cfg.OIDCEnabled(),
	}}, nil
}
