package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/clients"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/gmail"
)

type AccountOutput struct{ Body AccountSummary }
type AccountsOutput struct {
	Body struct {
		Items []AccountSummary `json:"items"`
	}
}

// AccountInput is the editable part of an account.
type AccountInput struct {
	Name string `json:"name" minLength:"1" maxLength:"100"`
	// DestinationID is the IMAP destination mail is delivered to.
	DestinationID string `json:"destination_id,omitempty" format:"uuid"`
}
type AccountCreateInput struct{ Body AccountInput }
type AccountUpdateInput struct {
	ID   string `path:"id"`
	Body AccountInput
}

func (a *API) accountSummary(ctx context.Context, acct store.Account) (AccountSummary, error) {
	s := AccountSummary{
		ID: acct.ID.String(), Name: acct.Name, GmailAddress: acct.GmailAddress, Status: acct.Status,
		StatusError: acct.LastConnectionError, LastConnectionCheck: ts(acct.LastConnectionCheck), AuthorisedAt: ts(acct.AuthorisedAt),
		DestinationID: uuidPtr(acct.DestinationID), CreatedAt: tsOrZero(acct.CreatedAt),
	}
	if acct.DestinationID.Valid {
		if d, err := a.db.GetDestination(ctx, acct.DestinationID.UUID); err == nil {
			s.DestinationName = d.Name
		}
	}
	if ss, err := a.db.GetSyncSettings(ctx, acct.ID); err == nil {
		s.SyncEnabled = ss.Enabled
	}
	if st, err := a.db.GetSyncState(ctx, acct.ID); err == nil {
		s.InitialSyncCompleted = st.InitialSyncCompleted
		s.LastSuccessfulSync = ts(st.LastSuccessfulSyncAt)
	}
	return s, nil
}

func (a *API) loadAccount(ctx context.Context, idStr string) (store.Account, error) {
	id, err := parseID(idStr, "account")
	if err != nil {
		return store.Account{}, err
	}
	acct, err := a.db.GetAccount(ctx, id)
	if err != nil {
		return store.Account{}, a.notFoundOr(err, "account")
	}
	return acct, nil
}

func (a *API) listAccounts(ctx context.Context, _ *struct{}) (*AccountsOutput, error) {
	rows, err := a.db.ListAccounts(ctx)
	if err != nil {
		return nil, a.internalError(err, "list accounts")
	}
	out := &AccountsOutput{}
	out.Body.Items = make([]AccountSummary, 0, len(rows))
	for _, r := range rows {
		s, err := a.accountSummary(ctx, r)
		if err != nil {
			return nil, a.internalError(err, "summarise account")
		}
		out.Body.Items = append(out.Body.Items, s)
	}
	return out, nil
}

func (a *API) getAccount(ctx context.Context, in *IDInput) (*AccountOutput, error) {
	acct, err := a.loadAccount(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	s, err := a.accountSummary(ctx, acct)
	if err != nil {
		return nil, a.internalError(err, "summarise account")
	}
	return &AccountOutput{Body: s}, nil
}

func (a *API) checkDestination(ctx context.Context, idStr string) (uuid.NullUUID, error) {
	n, err := nullUUID(idStr)
	if err != nil || !n.Valid {
		return n, err
	}
	if _, err := a.db.GetDestination(ctx, n.UUID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return n, huma.Error422UnprocessableEntity("destination not found")
		}
		return n, a.internalError(err, "load destination")
	}
	return n, nil
}

func (a *API) createAccount(ctx context.Context, in *AccountCreateInput) (*AccountOutput, error) {
	dest, err := a.checkDestination(ctx, in.Body.DestinationID)
	if err != nil {
		return nil, err
	}
	id := uuid.Must(uuid.NewV7())
	acct, err := a.db.CreateAccount(ctx, store.CreateAccountParams{ID: id, Name: in.Body.Name, DestinationID: dest})
	if isUniqueViolation(err) {
		return nil, huma.Error409Conflict("an account with this name already exists")
	}
	if err != nil {
		return nil, a.internalError(err, "create account")
	}
	if err := a.db.EnsureSyncSettings(ctx, id); err != nil {
		return nil, a.internalError(err, "create sync settings")
	}
	if err := a.db.EnsureSyncState(ctx, id); err != nil {
		return nil, a.internalError(err, "create sync state")
	}
	s, _ := a.accountSummary(ctx, acct)
	return &AccountOutput{Body: s}, nil
}

func (a *API) updateAccount(ctx context.Context, in *AccountUpdateInput) (*AccountOutput, error) {
	cur, err := a.loadAccount(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	dest, err := a.checkDestination(ctx, in.Body.DestinationID)
	if err != nil {
		return nil, err
	}
	acct, err := a.db.UpdateAccount(ctx, store.UpdateAccountParams{ID: cur.ID, Name: in.Body.Name, DestinationID: dest})
	if isUniqueViolation(err) {
		return nil, huma.Error409Conflict("an account with this name already exists")
	}
	if err != nil {
		return nil, a.internalError(err, "update account")
	}
	s, _ := a.accountSummary(ctx, acct)
	return &AccountOutput{Body: s}, nil
}

func (a *API) deleteAccount(ctx context.Context, in *IDInput) (*struct{}, error) {
	acct, err := a.loadAccount(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if active, err := a.db.GetActiveSession(ctx, acct.ID); err == nil && active.Status == "running" {
		return nil, huma.Error409Conflict("a sync is running for this account; cancel it first")
	}
	if err := a.db.DeleteAccount(ctx, acct.ID); err != nil {
		return nil, a.internalError(err, "delete account")
	}
	return nil, nil
}

// GmailTest and the IMAP half of an account test.
type GmailTest struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Email string `json:"email,omitempty" doc:"Address of the authorised Gmail account"`
}
type AccountTest struct {
	Gmail GmailTest      `json:"gmail"`
	IMAP  ConnectionTest `json:"imap"`
}
type AccountTestOutput struct{ Body AccountTest }

func (a *API) testAccount(ctx context.Context, in *IDInput) (*AccountTestOutput, error) {
	acct, err := a.loadAccount(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	var res AccountTest

	// Gmail.
	status, statusErr := acct.Status, ""
	func() {
		g, err := a.clients.Gmail(ctx, acct, nil)
		if err == nil {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			var p gmail.Profile
			if p, err = g.Profile(cctx); err == nil {
				res.Gmail = GmailTest{OK: true, Email: p.Email}
				if acct.Status != "disabled" {
					status = "ok"
				}
				return
			}
		}
		switch {
		case errors.Is(err, clients.ErrNotConfigured):
			res.Gmail.Error = "Google sign-in is not set up yet. Add your OAuth client in settings."
		case errors.Is(err, clients.ErrNotAuthorised):
			res.Gmail.Error = "This account has not been authorised with Google yet."
		case errors.Is(err, gmail.ErrAuth):
			res.Gmail.Error = "Google access was revoked or expired. Reauthorise this account."
			status, statusErr = "needs_reauth", res.Gmail.Error
		default:
			res.Gmail.Error = "Gmail could not be reached: " + shortError(err)
			statusErr = res.Gmail.Error
		}
	}()
	if res.Gmail.OK {
		statusErr = ""
	}
	if err := a.db.SetAccountStatus(ctx, store.SetAccountStatusParams{ID: acct.ID, Status: status, LastConnectionError: statusErr}); err != nil {
		a.logger.Warn("store connection check", "error", err)
	}

	// IMAP.
	switch {
	case !acct.DestinationID.Valid:
		res.IMAP = ConnectionTest{Error: "No destination is selected for this account."}
	default:
		d, err := a.db.GetDestination(ctx, acct.DestinationID.UUID)
		if err != nil {
			return nil, a.notFoundOr(err, "destination")
		}
		cfg, err := a.clients.IMAPConfig(d)
		if err != nil {
			return nil, a.internalError(err, "build imap config")
		}
		res.IMAP = a.runIMAPTest(ctx, cfg)
		if res.IMAP.OK {
			_ = a.db.SetDestinationDelimiter(ctx, store.SetDestinationDelimiterParams{ID: d.ID, HierarchyDelimiter: res.IMAP.Delimiter})
		}
	}
	return &AccountTestOutput{Body: res}, nil
}

func (a *API) registerAccounts(api huma.API) {
	tags := []string{"Accounts"}
	huma.Register(api, huma.Operation{OperationID: "list-accounts", Method: http.MethodGet, Path: "/api/v1/accounts",
		Summary: "List Gmail accounts", Tags: tags}, a.listAccounts)
	huma.Register(api, huma.Operation{OperationID: "create-account", Method: http.MethodPost, Path: "/api/v1/accounts",
		Summary: "Create a Gmail account", Tags: tags, DefaultStatus: http.StatusCreated}, a.createAccount)
	huma.Register(api, huma.Operation{OperationID: "get-account", Method: http.MethodGet, Path: "/api/v1/accounts/{id}",
		Summary: "Get a Gmail account", Tags: tags}, a.getAccount)
	huma.Register(api, huma.Operation{OperationID: "update-account", Method: http.MethodPut, Path: "/api/v1/accounts/{id}",
		Summary: "Update a Gmail account", Tags: tags}, a.updateAccount)
	huma.Register(api, huma.Operation{OperationID: "delete-account", Method: http.MethodDelete, Path: "/api/v1/accounts/{id}",
		Summary: "Remove a Gmail account", Description: "Removes the account, its settings, sessions and failure records. Nothing is deleted in Gmail or on the IMAP server.",
		Tags: tags, DefaultStatus: http.StatusNoContent}, a.deleteAccount)
	huma.Register(api, huma.Operation{OperationID: "test-account", Method: http.MethodPost, Path: "/api/v1/accounts/{id}/test",
		Summary: "Test the Gmail and IMAP connections of an account", Tags: tags}, a.testAccount)
}
