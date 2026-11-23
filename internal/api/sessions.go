package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
)

type SessionsInput struct {
	Account string    `query:"account" format:"uuid" doc:"Only sessions of this account"`
	Status  string    `query:"status" enum:"queued,running,completed,completed_with_errors,failed,cancelled,abandoned"`
	Trigger string    `query:"trigger" enum:"scheduled,manual,initial,retry,recovery"`
	Type    string    `query:"type" enum:"initial,incremental,reconcile,retry"`
	From    time.Time `query:"from" doc:"Created at or after (RFC 3339)"`
	To      time.Time `query:"to" doc:"Created before (RFC 3339)"`
	Cursor  string    `query:"cursor" doc:"next_cursor of the previous page"`
	Limit   int       `query:"limit" minimum:"0" maximum:"200" doc:"Page size, default 50"`
}

type SessionsOutput struct {
	Body struct {
		Items      []SessionSummary `json:"items"`
		NextCursor string           `json:"next_cursor,omitempty" doc:"Pass as cursor to get the next page; absent on the last page"`
	}
}

func (a *API) listSessions(ctx context.Context, in *SessionsInput) (*SessionsOutput, error) {
	acct, err := nullUUID(in.Account)
	if err != nil {
		return nil, err
	}
	p := store.ListSessionsParams{
		AccountID: acct, Status: text(in.Status), TriggerType: text(in.Trigger), SyncType: text(in.Type),
		From: tstz(in.From), To: tstz(in.To), Lim: pageSize(in.Limit) + 1,
	}
	if in.Cursor != "" {
		c, err := decodeCursor(in.Cursor)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid cursor")
		}
		p.BeforeCreated = pgtype.Timestamptz{Time: c.T, Valid: true}
		p.BeforeID = uuid.NullUUID{UUID: c.ID, Valid: true}
	}
	rows, err := a.db.ListSessions(ctx, p)
	if err != nil {
		return nil, a.internalError(err, "list sessions")
	}
	out := &SessionsOutput{}
	if n := int(pageSize(in.Limit)); len(rows) > n {
		rows = rows[:n]
		last := rows[n-1]
		out.Body.NextCursor = encodeCursor(last.CreatedAt.Time, last.ID)
	}
	out.Body.Items = sessionSummaries(rows)
	return out, nil
}

func (a *API) getSession(ctx context.Context, in *IDInput) (*SessionOutput, error) {
	id, err := parseID(in.ID, "session")
	if err != nil {
		return nil, err
	}
	s, err := a.db.GetSession(ctx, id)
	if err != nil {
		return nil, a.notFoundOr(err, "session")
	}
	return &SessionOutput{Body: sessionSummary(s)}, nil
}

// SessionEvent is one aggregated event of a session.
type SessionEvent struct {
	ID        int64          `json:"id"`
	Type      string         `json:"type"`
	Severity  string         `json:"severity" enum:"debug,info,warning,error"`
	Message   string         `json:"message"`
	Metadata  map[string]any `json:"metadata"`
	CreatedAt time.Time      `json:"created_at"`
}

type SessionEventsInput struct {
	ID    string `path:"id"`
	After int64  `query:"after" minimum:"0" doc:"Only events with a greater id"`
	Limit int    `query:"limit" minimum:"0" maximum:"1000" doc:"Default 200"`
}

type SessionEventsOutput struct {
	Body struct {
		Items []SessionEvent `json:"items"`
	}
}

func (a *API) listSessionEvents(ctx context.Context, in *SessionEventsInput) (*SessionEventsOutput, error) {
	id, err := parseID(in.ID, "session")
	if err != nil {
		return nil, err
	}
	if _, err := a.db.GetSession(ctx, id); err != nil {
		return nil, a.notFoundOr(err, "session")
	}
	limit := int32(in.Limit)
	if limit <= 0 {
		limit = 200
	}
	rows, err := a.db.ListEvents(ctx, store.ListEventsParams{SessionID: id, ID: in.After, Limit: limit})
	if err != nil {
		return nil, a.internalError(err, "list session events")
	}
	out := &SessionEventsOutput{}
	out.Body.Items = make([]SessionEvent, 0, len(rows))
	for _, r := range rows {
		out.Body.Items = append(out.Body.Items, SessionEvent{
			ID: r.ID, Type: r.EventType, Severity: r.Severity, Message: r.Message,
			Metadata: decodeObject(r.Metadata), CreatedAt: tsOrZero(r.CreatedAt),
		})
	}
	return out, nil
}

func (a *API) registerSessions(api huma.API) {
	tags := []string{"Sessions"}
	huma.Register(api, huma.Operation{OperationID: "list-sessions", Method: http.MethodGet, Path: "/api/v1/sessions",
		Summary: "List synchronisation sessions, newest first", Tags: tags}, a.listSessions)
	huma.Register(api, huma.Operation{OperationID: "get-session", Method: http.MethodGet, Path: "/api/v1/sessions/{id}",
		Summary: "Get a synchronisation session", Tags: tags}, a.getSession)
	huma.Register(api, huma.Operation{OperationID: "list-session-events", Method: http.MethodGet, Path: "/api/v1/sessions/{id}/events",
		Summary: "List the aggregated events of a session", Tags: tags}, a.listSessionEvents)
}
