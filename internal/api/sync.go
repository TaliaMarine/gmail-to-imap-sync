package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/progress"
)

// enqueueSession queues a session for the account. A second active session
// for one account violates a unique index; in that case the existing active
// session is returned and created is false.
func (a *API) enqueueSession(ctx context.Context, accountID uuid.UUID, trigger, syncType string, params any) (s store.SyncSession, created bool, err error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return s, false, err
	}
	s, err = a.db.CreateSession(ctx, store.CreateSessionParams{
		ID: uuid.Must(uuid.NewV7()), AccountID: accountID, TriggerType: trigger, SyncType: syncType, Params: raw,
	})
	if isUniqueViolation(err) {
		existing, gerr := a.db.GetActiveSession(ctx, accountID)
		if gerr != nil {
			return s, false, gerr
		}
		return existing, false, nil
	}
	if err != nil {
		return s, false, err
	}
	a.announce(ctx, progress.KindQueued, s)
	return s, true, nil
}

// announce tells every web pod (and this one directly) that a session changed.
func (a *API) announce(ctx context.Context, kind string, s store.SyncSession) {
	e := progress.Event{Kind: kind, AccountID: s.AccountID.String(), SessionID: s.ID.String()}
	if a.hub != nil {
		a.hub.Publish(e)
	}
	if err := progress.Notify(ctx, a.db.Pool, e); err != nil {
		a.logger.Warn("notify progress", "error", err)
	}
}

// SyncRequest starts a synchronisation of one account.
type SyncRequest struct {
	// Type incremental follows Gmail's history; reconcile re-scans a date
	// range. The first sync of an account is always an initial sync.
	Type    string  `json:"type,omitempty" enum:"incremental,reconcile" default:"incremental"`
	After   *string `json:"after,omitempty" format:"date" doc:"Reconcile only: re-scan messages received on or after this date"`
	LabelID *string `json:"label_id,omitempty" doc:"Reconcile only: limit the re-scan to one Gmail label"`
}

type SyncStartInput struct {
	ID   string `path:"id"`
	Body SyncRequest
}

type SyncStartOutput struct {
	Body struct {
		Session       SessionSummary `json:"session"`
		AlreadyActive bool           `json:"already_active" doc:"A session was already queued or running; it is returned instead of a new one"`
	}
}

func (a *API) startSync(ctx context.Context, in *SyncStartInput) (*SyncStartOutput, error) {
	acct, err := a.loadAccount(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if reason := a.syncBlocked(ctx, acct); reason != "" {
		return nil, huma.Error409Conflict(reason)
	}
	syncType := in.Body.Type
	if syncType == "" {
		syncType = "incremental"
	}
	if st, err := a.db.GetSyncState(ctx, acct.ID); err == nil && !st.InitialSyncCompleted {
		syncType = "initial"
	}
	params := map[string]any{}
	if syncType == "reconcile" {
		if in.Body.After != nil {
			if _, err := time.Parse(time.DateOnly, *in.Body.After); err != nil {
				return nil, huma.Error422UnprocessableEntity("after must be a date like 2024-01-31")
			}
			params["after"] = *in.Body.After
		}
		if in.Body.LabelID != nil && *in.Body.LabelID != "" {
			params["label_id"] = *in.Body.LabelID
		}
	}
	trigger := "manual"
	if syncType == "initial" {
		trigger = "initial"
	}
	s, created, err := a.enqueueSession(ctx, acct.ID, trigger, syncType, params)
	if err != nil {
		return nil, a.internalError(err, "enqueue sync")
	}
	out := &SyncStartOutput{}
	out.Body.Session, out.Body.AlreadyActive = sessionSummary(s), !created
	return out, nil
}

// syncBlocked explains why an account cannot be synchronised right now.
func (a *API) syncBlocked(ctx context.Context, acct store.Account) string {
	switch {
	case acct.Status == "unauthorised":
		return "This account has not been authorised with Google yet."
	case acct.Status == "needs_reauth":
		return "Google access was revoked or expired. Reauthorise this account first."
	case !acct.DestinationID.Valid:
		return "Choose a destination for this account first."
	}
	if d, err := a.db.GetDestination(ctx, acct.DestinationID.UUID); err == nil && !d.Enabled {
		return "The destination of this account is disabled."
	}
	return ""
}

type SessionOutput struct{ Body SessionSummary }

func (a *API) cancelSync(ctx context.Context, in *IDInput) (*SessionOutput, error) {
	acct, err := a.loadAccount(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	active, err := a.db.GetActiveSession(ctx, acct.ID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, huma.Error404NotFound("no sync is queued or running for this account")
		}
		return nil, a.internalError(err, "load active session")
	}
	s, err := a.db.RequestSessionCancel(ctx, active.ID)
	if err != nil {
		if err == pgx.ErrNoRows { // finished between the two queries
			return nil, huma.Error404NotFound("no sync is queued or running for this account")
		}
		return nil, a.internalError(err, "cancel session")
	}
	kind := progress.KindProgress
	if s.Status == "cancelled" {
		kind = progress.KindFinished
	}
	a.announce(ctx, kind, s)
	return &SessionOutput{Body: sessionSummary(s)}, nil
}

type CurrentSyncOutput struct {
	Body struct {
		Session *SessionSummary `json:"session,omitempty" doc:"The active session, else the most recent one; absent when the account never synchronised"`
		Active  bool            `json:"active"`
	}
}

func (a *API) currentSync(ctx context.Context, in *IDInput) (*CurrentSyncOutput, error) {
	acct, err := a.loadAccount(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	out := &CurrentSyncOutput{}
	s, err := a.db.GetActiveSession(ctx, acct.ID)
	if err == nil {
		v := sessionSummary(s)
		out.Body.Session, out.Body.Active = &v, true
		return out, nil
	}
	if err != pgx.ErrNoRows {
		return nil, a.internalError(err, "load active session")
	}
	rows, err := a.db.ListSessions(ctx, store.ListSessionsParams{AccountID: uuid.NullUUID{UUID: acct.ID, Valid: true}, Lim: 1})
	if err != nil {
		return nil, a.internalError(err, "load last session")
	}
	if len(rows) > 0 {
		v := sessionSummary(rows[0])
		out.Body.Session = &v
	}
	return out, nil
}

func (a *API) registerSync(api huma.API) {
	tags := []string{"Sync"}
	huma.Register(api, huma.Operation{OperationID: "start-sync", Method: http.MethodPost, Path: "/api/v1/accounts/{id}/sync",
		Summary:     "Start a synchronisation",
		Description: "Queues a session. If one is already queued or running it is returned with already_active set.",
		Tags:        tags}, a.startSync)
	huma.Register(api, huma.Operation{OperationID: "cancel-sync", Method: http.MethodPost, Path: "/api/v1/accounts/{id}/sync/cancel",
		Summary:     "Cancel the active synchronisation",
		Description: "Graceful: the message in flight finishes and the checkpoint is saved.",
		Tags:        tags}, a.cancelSync)
	huma.Register(api, huma.Operation{OperationID: "get-current-sync", Method: http.MethodGet, Path: "/api/v1/accounts/{id}/sync/current",
		Summary: "Get the active or most recent synchronisation", Tags: tags}, a.currentSync)
}
