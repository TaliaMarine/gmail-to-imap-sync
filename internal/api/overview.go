package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
)

type FailureCounts struct {
	Pending  int64 `json:"pending"`
	Retrying int64 `json:"retrying"`
	Ignored  int64 `json:"ignored"`
}

// AccountOverview is the dashboard card of one account.
type AccountOverview struct {
	AccountSummary
	NextScheduledSync *time.Time      `json:"next_scheduled_sync,omitempty" doc:"Absent when scheduling is off or the account is not ready"`
	ActiveSession     *SessionSummary `json:"active_session,omitempty"`
	LastSession       *SessionSummary `json:"last_session,omitempty"`
	Failures          FailureCounts   `json:"failures"`
}

type ActivityDay struct {
	Day      time.Time `json:"day"`
	Copied   int64     `json:"copied"`
	Skipped  int64     `json:"skipped"`
	Failed   int64     `json:"failed"`
	Sessions int64     `json:"sessions"`
}

type OverviewOutput struct {
	Body struct {
		Accounts        []AccountOverview `json:"accounts"`
		Activity        []ActivityDay     `json:"activity" doc:"Last 14 days, oldest first, days without sessions included"`
		RecentSessions  []SessionSummary  `json:"recent_sessions"`
		RecentFailures  []Failure         `json:"recent_failures"`
		PendingFailures int64             `json:"pending_failures"`
	}
}

const activityDays = 14

func (a *API) getOverview(ctx context.Context, _ *struct{}) (*OverviewOutput, error) {
	accounts, err := a.db.ListAccounts(ctx)
	if err != nil {
		return nil, a.internalError(err, "list accounts")
	}
	counts, err := a.db.CountFailures(ctx)
	if err != nil {
		return nil, a.internalError(err, "count failures")
	}
	failures := map[uuid.UUID]*FailureCounts{}
	var pending int64
	for _, c := range counts {
		fc := failures[c.AccountID]
		if fc == nil {
			fc = &FailureCounts{}
			failures[c.AccountID] = fc
		}
		switch c.Status {
		case "pending":
			fc.Pending, pending = c.N, pending+c.N
		case "retrying":
			fc.Retrying, pending = c.N, pending+c.N
		case "ignored":
			fc.Ignored = c.N
		}
	}
	scheduled, err := a.db.ListScheduledAccounts(ctx)
	if err != nil {
		return nil, a.internalError(err, "list scheduled accounts")
	}
	next := map[uuid.UUID]time.Time{}
	for _, s := range scheduled {
		interval := time.Duration(s.ScheduleIntervalSeconds) * time.Second
		last := s.LastScheduled.Time
		if !s.LastScheduled.Valid || last.IsZero() {
			next[s.AccountID] = time.Now()
		} else {
			next[s.AccountID] = last.Add(interval).UTC()
		}
	}

	out := &OverviewOutput{}
	out.Body.Accounts = make([]AccountOverview, 0, len(accounts))
	for _, acct := range accounts {
		sum, _ := a.accountSummary(ctx, acct)
		ov := AccountOverview{AccountSummary: sum}
		if t, ok := next[acct.ID]; ok {
			ov.NextScheduledSync = &t
		}
		if fc := failures[acct.ID]; fc != nil {
			ov.Failures = *fc
		}
		if s, err := a.db.GetActiveSession(ctx, acct.ID); err == nil {
			v := sessionSummary(s)
			ov.ActiveSession = &v
		}
		if rows, err := a.db.ListSessions(ctx, store.ListSessionsParams{
			AccountID: uuid.NullUUID{UUID: acct.ID, Valid: true}, Status: text("completed"), Lim: 1,
		}); err == nil && len(rows) > 0 {
			v := sessionSummary(rows[0])
			ov.LastSession = &v
		}
		out.Body.Accounts = append(out.Body.Accounts, ov)
	}
	out.Body.PendingFailures = pending

	since := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -(activityDays - 1))
	rows, err := a.db.SessionActivityByDay(ctx, tstz(since))
	if err != nil {
		return nil, a.internalError(err, "load activity")
	}
	out.Body.Activity = fillActivity(rows, since, activityDays)

	recent, err := a.db.ListSessions(ctx, store.ListSessionsParams{Lim: 10})
	if err != nil {
		return nil, a.internalError(err, "list recent sessions")
	}
	out.Body.RecentSessions = sessionSummaries(recent)
	fails, err := a.db.ListFailures(ctx, store.ListFailuresParams{Status: text("pending"), Lim: 10})
	if err != nil {
		return nil, a.internalError(err, "list recent failures")
	}
	out.Body.RecentFailures = make([]Failure, 0, len(fails))
	for _, f := range fails {
		out.Body.RecentFailures = append(out.Body.RecentFailures, failureBody(f))
	}
	return out, nil
}

// fillActivity returns one entry per day starting at since, with zeros for
// days that had no sessions.
func fillActivity(rows []store.SessionActivityByDayRow, since time.Time, days int) []ActivityDay {
	byDay := map[string]store.SessionActivityByDayRow{}
	for _, r := range rows {
		byDay[r.Day.Time.UTC().Format(time.DateOnly)] = r
	}
	out := make([]ActivityDay, 0, days)
	for i := 0; i < days; i++ {
		d := since.AddDate(0, 0, i)
		r := byDay[d.Format(time.DateOnly)]
		out = append(out, ActivityDay{Day: d, Copied: r.Copied, Skipped: r.Skipped, Failed: r.Failed, Sessions: r.Sessions})
	}
	return out
}

func (a *API) registerOverview(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "get-overview", Method: http.MethodGet, Path: "/api/v1/overview",
		Summary: "Dashboard data", Tags: []string{"Overview"}}, a.getOverview)
}
