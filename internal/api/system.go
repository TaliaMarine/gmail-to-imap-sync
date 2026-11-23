package api

import (
	"context"
	"time"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/version"
)

type Check struct {
	OK    bool   `json:"ok" doc:"Whether the check passed"`
	Error string `json:"error,omitempty" doc:"Why the check failed"`
}

type SystemStatus struct {
	Version           string     `json:"version" doc:"Application version"`
	Mode              string     `json:"mode" doc:"Process mode: all, web or worker"`
	UptimeSeconds     int64      `json:"uptime_seconds" doc:"Seconds since this process started"`
	Database          Check      `json:"database" doc:"PostgreSQL connectivity"`
	OAuthNeedsReauth  int64      `json:"oauth_needs_reauth" doc:"Accounts whose Google access must be renewed"`
	SessionsQueued    int64      `json:"sessions_queued"`
	SessionsRunning   int64      `json:"sessions_running"`
	LastSuccessfulRun *time.Time `json:"last_successful_session,omitempty" doc:"When a session last finished without errors"`
}

type SystemStatusOutput struct{ Body SystemStatus }

func (a *API) getSystemStatus(ctx context.Context, _ *struct{}) (*SystemStatusOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	s := SystemStatus{
		Version:       version.Version,
		Mode:          string(a.mode),
		UptimeSeconds: int64(time.Since(a.startedAt).Seconds()),
		Database:      Check{OK: true},
	}
	if err := a.db.Ping(ctx); err != nil {
		s.Database = Check{Error: "database unreachable"}
		a.logger.Warn("database check failed", "error", err)
		return &SystemStatusOutput{Body: s}, nil
	}
	var last *time.Time
	err := a.queryRow(ctx, `SELECT
		(SELECT count(*) FROM accounts WHERE status = 'needs_reauth'),
		(SELECT count(*) FROM sync_sessions WHERE status = 'queued'),
		(SELECT count(*) FROM sync_sessions WHERE status = 'running'),
		(SELECT max(finished_at) FROM sync_sessions WHERE status = 'completed')`).
		Scan(&s.OAuthNeedsReauth, &s.SessionsQueued, &s.SessionsRunning, &last)
	if err != nil {
		a.logger.Warn("status counters failed", "error", err)
	}
	if last != nil {
		u := last.UTC()
		s.LastSuccessfulRun = &u
	}
	return &SystemStatusOutput{Body: s}, nil
}
