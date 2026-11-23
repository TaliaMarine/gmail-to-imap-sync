package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
)

// failureCategory describes an error category in the words the UI shows.
type failureCategory struct {
	Description string
	AutoRetry   bool // retried automatically in later sessions
}

var failureCategories = map[string]failureCategory{
	"gmail_rate_limit":          {"Gmail asked us to slow down. This is retried automatically.", true},
	"gmail_auth":                {"Google access was revoked or expired. Reauthorise the account, then retry.", false},
	"gmail_fetch":               {"The message could not be downloaded from Gmail. This is retried automatically.", true},
	"gmail_message_unavailable": {"The message no longer exists in Gmail, or is too large to copy.", false},
	"imap_auth":                 {"The IMAP server rejected the login. Check the destination's username and password, then retry.", false},
	"imap_connection":           {"The IMAP server could not be reached. This is retried automatically.", true},
	"imap_folder":               {"A destination folder could not be created or opened. This is retried automatically.", true},
	"imap_append":               {"The IMAP server refused the message, for example because the mailbox is full. This is retried automatically.", true},
	"parse":                     {"The message is malformed and cannot be delivered as it is.", false},
	"config":                    {"The synchronisation settings are invalid. Fix them, then retry.", false},
	"unknown":                   {"An unexpected error occurred. This is retried automatically.", true},
}

func categoryInfo(c string) failureCategory {
	if v, ok := failureCategories[c]; ok {
		return v
	}
	return failureCategories["unknown"]
}

// Failure is a message that could not be delivered. Message content is never stored.
type Failure struct {
	ID               string     `json:"id" format:"uuid"`
	AccountID        string     `json:"account_id" format:"uuid"`
	GmailMessageID   string     `json:"gmail_message_id"`
	GmailThreadID    string     `json:"gmail_thread_id"`
	Subject          string     `json:"subject"`
	Sender           string     `json:"sender"`
	MessageDate      *time.Time `json:"message_date,omitempty"`
	IntendedFolders  []string   `json:"intended_folders"`
	CompletedFolders []string   `json:"completed_folders"`
	FailedFolders    []string   `json:"failed_folders"`
	Category         string     `json:"category"`
	Description      string     `json:"description" doc:"What the category means and what to do"`
	AutoRetry        bool       `json:"auto_retry" doc:"Whether later sessions retry this automatically; a manual retry is always possible"`
	ErrorMessage     string     `json:"error_message"`
	Status           string     `json:"status" enum:"pending,retrying,ignored"`
	RetryExhausted   bool       `json:"retry_exhausted"`
	AttemptCount     int32      `json:"attempt_count"`
	NextRetryAt      *time.Time `json:"next_retry_at,omitempty"`
	FirstFailedAt    time.Time  `json:"first_failed_at"`
	LastFailedAt     time.Time  `json:"last_failed_at"`
	LastAttemptedAt  time.Time  `json:"last_attempted_at"`
	LastSessionID    *string    `json:"last_session_id,omitempty" format:"uuid"`
}

// FailureDetail adds the recipients and the error history.
type FailureDetail struct {
	Failure
	Recipients   []string        `json:"recipients"`
	ErrorHistory json.RawMessage `json:"error_history" doc:"Most recent attempts, newest first: time, category, message, session"`
}

func failureBody(f store.FailedMessage) Failure {
	info := categoryInfo(f.ErrorCategory)
	return Failure{
		ID: f.ID.String(), AccountID: f.AccountID.String(), GmailMessageID: f.GmailMessageID, GmailThreadID: f.GmailThreadID,
		Subject: f.Subject, Sender: f.Sender, MessageDate: ts(f.MessageDate),
		IntendedFolders: decodeStrings(f.IntendedFolders), CompletedFolders: decodeStrings(f.CompletedFolders),
		FailedFolders: decodeStrings(f.FailedFolders),
		Category:      f.ErrorCategory, Description: info.Description, AutoRetry: info.AutoRetry,
		ErrorMessage: f.ErrorMessage, Status: f.Status, RetryExhausted: f.RetryExhausted, AttemptCount: f.AttemptCount,
		NextRetryAt: ts(f.NextRetryAt), FirstFailedAt: tsOrZero(f.FirstFailedAt), LastFailedAt: tsOrZero(f.LastFailedAt),
		LastAttemptedAt: tsOrZero(f.LastAttemptedAt), LastSessionID: uuidPtr(f.LastSessionID),
	}
}

func failureDetail(f store.FailedMessage) FailureDetail {
	hist := json.RawMessage(f.ErrorHistory)
	if len(hist) == 0 {
		hist = json.RawMessage("[]")
	}
	return FailureDetail{Failure: failureBody(f), Recipients: decodeStrings(f.Recipients), ErrorHistory: hist}
}

type FailuresInput struct {
	Account  string `query:"account" format:"uuid"`
	Status   string `query:"status" enum:"pending,retrying,ignored"`
	Category string `query:"category" doc:"Error category, e.g. imap_append"`
	Search   string `query:"search" maxLength:"200" doc:"Matches subject or sender"`
	Cursor   string `query:"cursor"`
	Limit    int    `query:"limit" minimum:"0" maximum:"200"`
}

type FailuresOutput struct {
	Body struct {
		Items      []Failure `json:"items"`
		NextCursor string    `json:"next_cursor,omitempty"`
	}
}

func (a *API) listFailures(ctx context.Context, in *FailuresInput) (*FailuresOutput, error) {
	acct, err := nullUUID(in.Account)
	if err != nil {
		return nil, err
	}
	p := store.ListFailuresParams{
		AccountID: acct, Status: text(in.Status), Category: text(in.Category), Search: text(in.Search),
		Lim: pageSize(in.Limit) + 1,
	}
	if in.Cursor != "" {
		c, err := decodeCursor(in.Cursor)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid cursor")
		}
		p.BeforeFailed = pgtype.Timestamptz{Time: c.T, Valid: true}
		p.BeforeID = uuid.NullUUID{UUID: c.ID, Valid: true}
	}
	rows, err := a.db.ListFailures(ctx, p)
	if err != nil {
		return nil, a.internalError(err, "list failures")
	}
	out := &FailuresOutput{}
	if n := int(pageSize(in.Limit)); len(rows) > n {
		rows = rows[:n]
		out.Body.NextCursor = encodeCursor(rows[n-1].LastFailedAt.Time, rows[n-1].ID)
	}
	out.Body.Items = make([]Failure, 0, len(rows))
	for _, r := range rows {
		out.Body.Items = append(out.Body.Items, failureBody(r))
	}
	return out, nil
}

type FailureDetailOutput struct{ Body FailureDetail }

func (a *API) getFailure(ctx context.Context, in *IDInput) (*FailureDetailOutput, error) {
	id, err := parseID(in.ID, "failure")
	if err != nil {
		return nil, err
	}
	f, err := a.db.GetFailure(ctx, id)
	if err != nil {
		return nil, a.notFoundOr(err, "failure")
	}
	return &FailureDetailOutput{Body: failureDetail(f)}, nil
}

// RetrySession reports what happened for one account when failures were retried.
type RetrySession struct {
	AccountID string  `json:"account_id" format:"uuid"`
	SessionID *string `json:"session_id,omitempty" format:"uuid"`
	Queued    bool    `json:"queued" doc:"A new retry session was queued"`
	Reason    string  `json:"reason,omitempty" doc:"Why no new session was queued, e.g. one is already active (it picks the failures up automatically)"`
}

type RetryOutput struct {
	Body struct {
		Reset    int            `json:"reset" doc:"Failures made eligible for retry"`
		Sessions []RetrySession `json:"sessions"`
	}
}

// retryFailures makes the failures eligible again and queues one retry
// session per account.
func (a *API) retryFailures(ctx context.Context, fs []store.FailedMessage) (*RetryOutput, error) {
	out := &RetryOutput{}
	out.Body.Sessions = []RetrySession{}
	byAccount := map[uuid.UUID][]string{}
	var order []uuid.UUID
	for _, f := range fs {
		if err := a.db.ResetFailureForRetry(ctx, f.ID); err != nil {
			return nil, a.internalError(err, "reset failure")
		}
		out.Body.Reset++
		if _, ok := byAccount[f.AccountID]; !ok {
			order = append(order, f.AccountID)
		}
		byAccount[f.AccountID] = append(byAccount[f.AccountID], f.ID.String())
	}
	for _, accID := range order {
		rs := RetrySession{AccountID: accID.String()}
		acct, err := a.db.GetAccount(ctx, accID)
		if err != nil {
			rs.Reason = "account not found"
			out.Body.Sessions = append(out.Body.Sessions, rs)
			continue
		}
		if reason := a.syncBlocked(ctx, acct); reason != "" {
			rs.Reason = reason
			out.Body.Sessions = append(out.Body.Sessions, rs)
			continue
		}
		s, created, err := a.enqueueSession(ctx, accID, "retry", "retry", map[string]any{"failure_ids": byAccount[accID]})
		if err != nil {
			return nil, a.internalError(err, "enqueue retry")
		}
		id := s.ID.String()
		rs.SessionID, rs.Queued = &id, created
		if !created {
			rs.Reason = "a sync is already queued or running; it will retry these failures automatically"
		}
		out.Body.Sessions = append(out.Body.Sessions, rs)
	}
	return out, nil
}

func (a *API) retryFailure(ctx context.Context, in *IDInput) (*RetryOutput, error) {
	id, err := parseID(in.ID, "failure")
	if err != nil {
		return nil, err
	}
	f, err := a.db.GetFailure(ctx, id)
	if err != nil {
		return nil, a.notFoundOr(err, "failure")
	}
	return a.retryFailures(ctx, []store.FailedMessage{f})
}

type RetryManyInput struct {
	Body struct {
		IDs       []string `json:"ids,omitempty" maxItems:"1000" doc:"Failures to retry"`
		All       bool     `json:"all,omitempty" doc:"Retry every pending failure instead"`
		AccountID string   `json:"account_id,omitempty" format:"uuid" doc:"With all: only this account"`
	}
}

func (a *API) retryManyFailures(ctx context.Context, in *RetryManyInput) (*RetryOutput, error) {
	var ids []uuid.UUID
	switch {
	case in.Body.All:
		acct, err := nullUUID(in.Body.AccountID)
		if err != nil {
			return nil, err
		}
		if ids, err = a.db.PendingFailureIDs(ctx, acct); err != nil {
			return nil, a.internalError(err, "list pending failures")
		}
	case len(in.Body.IDs) > 0:
		for _, s := range in.Body.IDs {
			id, err := parseID(s, "failure")
			if err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
	default:
		return nil, huma.Error422UnprocessableEntity("give ids or set all")
	}
	if len(ids) == 0 {
		out := &RetryOutput{}
		out.Body.Sessions = []RetrySession{}
		return out, nil
	}
	fs, err := a.db.FailuresByIDs(ctx, ids)
	if err != nil {
		return nil, a.internalError(err, "load failures")
	}
	return a.retryFailures(ctx, fs)
}

func (a *API) setFailureStatus(status string) func(context.Context, *IDInput) (*FailureDetailOutput, error) {
	return func(ctx context.Context, in *IDInput) (*FailureDetailOutput, error) {
		id, err := parseID(in.ID, "failure")
		if err != nil {
			return nil, err
		}
		f, err := a.db.SetFailureStatus(ctx, store.SetFailureStatusParams{ID: id, Status: status})
		if err != nil {
			return nil, a.notFoundOr(err, "failure")
		}
		return &FailureDetailOutput{Body: failureDetail(f)}, nil
	}
}

type ClearIgnoredInput struct {
	Body struct {
		AccountID string `json:"account_id,omitempty" format:"uuid"`
	}
}
type ClearIgnoredOutput struct {
	Body struct {
		Cleared int64 `json:"cleared"`
	}
}

func (a *API) clearIgnored(ctx context.Context, in *ClearIgnoredInput) (*ClearIgnoredOutput, error) {
	acct, err := nullUUID(in.Body.AccountID)
	if err != nil {
		return nil, err
	}
	n, err := a.db.ClearIgnoredFailures(ctx, acct)
	if err != nil {
		return nil, a.internalError(err, "clear ignored failures")
	}
	out := &ClearIgnoredOutput{}
	out.Body.Cleared = n
	return out, nil
}

func (a *API) registerFailures(api huma.API) {
	tags := []string{"Failures"}
	huma.Register(api, huma.Operation{OperationID: "list-failures", Method: http.MethodGet, Path: "/api/v1/failures",
		Summary: "List failed messages", Tags: tags}, a.listFailures)
	huma.Register(api, huma.Operation{OperationID: "retry-failures", Method: http.MethodPost, Path: "/api/v1/failures/retry",
		Summary: "Retry several failed messages", Tags: tags}, a.retryManyFailures)
	huma.Register(api, huma.Operation{OperationID: "clear-ignored-failures", Method: http.MethodPost, Path: "/api/v1/failures/clear-ignored",
		Summary: "Remove all ignored failure records", Tags: tags}, a.clearIgnored)
	huma.Register(api, huma.Operation{OperationID: "get-failure", Method: http.MethodGet, Path: "/api/v1/failures/{id}",
		Summary: "Get a failed message with its error history", Tags: tags}, a.getFailure)
	huma.Register(api, huma.Operation{OperationID: "retry-failure", Method: http.MethodPost, Path: "/api/v1/failures/{id}/retry",
		Summary: "Retry one failed message", Tags: tags}, a.retryFailure)
	huma.Register(api, huma.Operation{OperationID: "ignore-failure", Method: http.MethodPost, Path: "/api/v1/failures/{id}/ignore",
		Summary: "Ignore a failed message", Tags: tags}, a.setFailureStatus("ignored"))
	huma.Register(api, huma.Operation{OperationID: "unignore-failure", Method: http.MethodPost, Path: "/api/v1/failures/{id}/unignore",
		Summary: "Stop ignoring a failed message", Tags: tags}, a.setFailureStatus("pending"))
}
