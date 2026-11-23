package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/imapdest"
)

// Destination is an IMAP server that archived mail is delivered to.
type Destination struct {
	ID                 string    `json:"id" format:"uuid"`
	Name               string    `json:"name"`
	Host               string    `json:"host"`
	Port               int32     `json:"port"`
	TLSMode            string    `json:"tls_mode" enum:"implicit,starttls,none"`
	TLSSkipVerify      bool      `json:"tls_skip_verify" doc:"Certificates are not verified; shown as a warning"`
	Username           string    `json:"username"`
	PasswordSet        bool      `json:"password_set" doc:"Whether a password is stored (it is never returned)"`
	HierarchyDelimiter string    `json:"hierarchy_delimiter" doc:"Learned from the server on a successful test"`
	Enabled            bool      `json:"enabled"`
	AccountCount       int64     `json:"account_count" doc:"Accounts delivering to this destination"`
	CreatedAt          time.Time `json:"created_at"`
}

// DestinationConfig is the editable part of a destination.
type DestinationConfig struct {
	Name          string `json:"name" minLength:"1" maxLength:"100"`
	Host          string `json:"host" minLength:"1" maxLength:"253"`
	Port          int32  `json:"port" minimum:"1" maximum:"65535"`
	TLSMode       string `json:"tls_mode" enum:"implicit,starttls,none"`
	TLSSkipVerify bool   `json:"tls_skip_verify"`
	Username      string `json:"username" minLength:"1" maxLength:"256"`
	// Password replaces the stored password; omit it on update to keep the
	// current one. Required on create.
	Password *string `json:"password,omitempty" maxLength:"512"`
	Enabled  bool    `json:"enabled"`
}

type DestinationOutput struct{ Body Destination }
type DestinationsOutput struct {
	Body struct {
		Items []Destination `json:"items"`
	}
}
type DestinationCreateInput struct{ Body DestinationConfig }
type DestinationUpdateInput struct {
	ID   string `path:"id"`
	Body DestinationConfig
}
type IDInput struct {
	ID string `path:"id"`
}

func (a *API) destinationBody(d store.Destination, accounts int64) Destination {
	return Destination{
		ID: d.ID.String(), Name: d.Name, Host: d.Host, Port: d.Port, TLSMode: d.TlsMode,
		TLSSkipVerify: d.TlsSkipVerify, Username: d.Username, PasswordSet: d.EncPassword != "",
		HierarchyDelimiter: d.HierarchyDelimiter, Enabled: d.Enabled, AccountCount: accounts,
		CreatedAt: tsOrZero(d.CreatedAt),
	}
}

func (a *API) accountCounts(ctx context.Context) (map[uuid.UUID]int64, error) {
	rows, err := a.db.Pool.Query(ctx, `SELECT destination_id, count(*) FROM accounts WHERE destination_id IS NOT NULL GROUP BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]int64{}
	for rows.Next() {
		var id uuid.UUID
		var n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

func (a *API) listDestinations(ctx context.Context, _ *struct{}) (*DestinationsOutput, error) {
	rows, err := a.db.ListDestinations(ctx)
	if err != nil {
		return nil, a.internalError(err, "list destinations")
	}
	counts, err := a.accountCounts(ctx)
	if err != nil {
		return nil, a.internalError(err, "count destination accounts")
	}
	out := &DestinationsOutput{}
	out.Body.Items = make([]Destination, 0, len(rows))
	for _, d := range rows {
		out.Body.Items = append(out.Body.Items, a.destinationBody(d, counts[d.ID]))
	}
	return out, nil
}

func (a *API) getDestination(ctx context.Context, in *IDInput) (*DestinationOutput, error) {
	id, err := parseID(in.ID, "destination")
	if err != nil {
		return nil, err
	}
	d, err := a.db.GetDestination(ctx, id)
	if err != nil {
		return nil, a.notFoundOr(err, "destination")
	}
	counts, err := a.accountCounts(ctx)
	if err != nil {
		return nil, a.internalError(err, "count destination accounts")
	}
	return &DestinationOutput{Body: a.destinationBody(d, counts[id])}, nil
}

func (a *API) createDestination(ctx context.Context, in *DestinationCreateInput) (*DestinationOutput, error) {
	if in.Body.Password == nil || *in.Body.Password == "" {
		return nil, huma.Error422UnprocessableEntity("password is required")
	}
	id := uuid.Must(uuid.NewV7())
	enc, err := a.clients.SealPassword(id, *in.Body.Password)
	if err != nil {
		return nil, a.internalError(err, "encrypt password")
	}
	d, err := a.db.CreateDestination(ctx, store.CreateDestinationParams{
		ID: id, Name: in.Body.Name, Host: in.Body.Host, Port: in.Body.Port, TlsMode: in.Body.TLSMode,
		TlsSkipVerify: in.Body.TLSSkipVerify, Username: in.Body.Username, EncPassword: enc, Enabled: in.Body.Enabled,
	})
	if isUniqueViolation(err) {
		return nil, huma.Error409Conflict("a destination with this name already exists")
	}
	if err != nil {
		return nil, a.internalError(err, "create destination")
	}
	return &DestinationOutput{Body: a.destinationBody(d, 0)}, nil
}

func (a *API) updateDestination(ctx context.Context, in *DestinationUpdateInput) (*DestinationOutput, error) {
	id, err := parseID(in.ID, "destination")
	if err != nil {
		return nil, err
	}
	cur, err := a.db.GetDestination(ctx, id)
	if err != nil {
		return nil, a.notFoundOr(err, "destination")
	}
	enc := cur.EncPassword
	if in.Body.Password != nil {
		if *in.Body.Password == "" {
			return nil, huma.Error422UnprocessableEntity("password cannot be empty; omit it to keep the current one")
		}
		if enc, err = a.clients.SealPassword(id, *in.Body.Password); err != nil {
			return nil, a.internalError(err, "encrypt password")
		}
	}
	if in.Body.Password == nil && !sameServer(cur, in.Body.Host, in.Body.Port, in.Body.Username, in.Body.TLSMode) {
		// The stored password must never follow the connection to another server.
		return nil, huma.Error422UnprocessableEntity("enter the password again when you change the host, port, username or TLS mode")
	}
	d, err := a.db.UpdateDestination(ctx, store.UpdateDestinationParams{
		ID: id, Name: in.Body.Name, Host: in.Body.Host, Port: in.Body.Port, TlsMode: in.Body.TLSMode,
		TlsSkipVerify: in.Body.TLSSkipVerify, Username: in.Body.Username, EncPassword: enc, Enabled: in.Body.Enabled,
	})
	if isUniqueViolation(err) {
		return nil, huma.Error409Conflict("a destination with this name already exists")
	}
	if err != nil {
		return nil, a.internalError(err, "update destination")
	}
	counts, _ := a.accountCounts(ctx)
	return &DestinationOutput{Body: a.destinationBody(d, counts[id])}, nil
}

func (a *API) deleteDestination(ctx context.Context, in *IDInput) (*struct{}, error) {
	id, err := parseID(in.ID, "destination")
	if err != nil {
		return nil, err
	}
	if _, err := a.db.GetDestination(ctx, id); err != nil {
		return nil, a.notFoundOr(err, "destination")
	}
	err = a.db.DeleteDestination(ctx, id)
	if isFKViolation(err) {
		return nil, huma.Error409Conflict("accounts still deliver to this destination; move them first")
	}
	if err != nil {
		return nil, a.internalError(err, "delete destination")
	}
	return nil, nil
}

// ConnectionTest is the result of testing a destination.
type ConnectionTest struct {
	OK        bool     `json:"ok"`
	Error     string   `json:"error,omitempty" doc:"Why the test failed"`
	ErrorKind string   `json:"error_kind,omitempty" enum:"imap_auth,imap_connection,imap_folder,imap_append,parse"`
	Delimiter string   `json:"delimiter,omitempty" doc:"Hierarchy delimiter reported by the server"`
	Folders   []string `json:"folders,omitempty" doc:"Existing folders"`
}

type ConnectionTestOutput struct{ Body ConnectionTest }

// DestinationTestInput tests an unsaved configuration. When destination_id is
// given and the password is omitted, the stored password is used.
type DestinationTestInput struct {
	Body struct {
		DestinationConfig
		DestinationID string `json:"destination_id,omitempty" format:"uuid"`
	}
}

func (a *API) runIMAPTest(ctx context.Context, cfg imapdest.Config) ConnectionTest {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	folders, delim, err := imapdest.CheckConnection(ctx, cfg)
	if err != nil {
		return ConnectionTest{Error: shortError(err), ErrorKind: string(imapdest.KindOf(err))}
	}
	return ConnectionTest{OK: true, Delimiter: delim, Folders: folders}
}

func (a *API) testStoredDestination(ctx context.Context, in *IDInput) (*ConnectionTestOutput, error) {
	id, err := parseID(in.ID, "destination")
	if err != nil {
		return nil, err
	}
	d, err := a.db.GetDestination(ctx, id)
	if err != nil {
		return nil, a.notFoundOr(err, "destination")
	}
	cfg, err := a.clients.IMAPConfig(d)
	if err != nil {
		return nil, a.internalError(err, "build imap config")
	}
	res := a.runIMAPTest(ctx, cfg)
	if res.OK {
		_ = a.db.SetDestinationDelimiter(ctx, store.SetDestinationDelimiterParams{ID: id, HierarchyDelimiter: res.Delimiter})
	}
	return &ConnectionTestOutput{Body: res}, nil
}

func (a *API) testDraftDestination(ctx context.Context, in *DestinationTestInput) (*ConnectionTestOutput, error) {
	cfg := imapdest.Config{
		Host: in.Body.Host, Port: int(in.Body.Port), TLSMode: in.Body.TLSMode,
		SkipVerify: in.Body.TLSSkipVerify, Username: in.Body.Username,
	}
	if in.Body.Password != nil {
		cfg.Password = *in.Body.Password
	}
	if in.Body.DestinationID != "" && in.Body.Password == nil {
		id, err := parseID(in.Body.DestinationID, "destination")
		if err != nil {
			return nil, err
		}
		d, err := a.db.GetDestination(ctx, id)
		if err != nil {
			return nil, a.notFoundOr(err, "destination")
		}
		if !sameServer(d, in.Body.Host, in.Body.Port, in.Body.Username, in.Body.TLSMode) {
			return nil, huma.Error422UnprocessableEntity("enter the password again when you change the host, port, username or TLS mode")
		}
		stored, err := a.clients.IMAPConfig(d)
		if err != nil {
			return nil, a.internalError(err, "build imap config")
		}
		cfg.Password = stored.Password
	}
	return &ConnectionTestOutput{Body: a.runIMAPTest(ctx, cfg)}, nil
}

func (a *API) registerDestinations(api huma.API) {
	tags := []string{"Destinations"}
	huma.Register(api, huma.Operation{OperationID: "list-destinations", Method: http.MethodGet, Path: "/api/v1/destinations",
		Summary: "List destinations", Tags: tags}, a.listDestinations)
	huma.Register(api, huma.Operation{OperationID: "create-destination", Method: http.MethodPost, Path: "/api/v1/destinations",
		Summary: "Create a destination", Tags: tags, DefaultStatus: http.StatusCreated}, a.createDestination)
	huma.Register(api, huma.Operation{OperationID: "test-destination-config", Method: http.MethodPost, Path: "/api/v1/destinations/test",
		Summary: "Test an unsaved destination configuration", Tags: tags}, a.testDraftDestination)
	huma.Register(api, huma.Operation{OperationID: "get-destination", Method: http.MethodGet, Path: "/api/v1/destinations/{id}",
		Summary: "Get a destination", Tags: tags}, a.getDestination)
	huma.Register(api, huma.Operation{OperationID: "update-destination", Method: http.MethodPut, Path: "/api/v1/destinations/{id}",
		Summary: "Update a destination", Tags: tags}, a.updateDestination)
	huma.Register(api, huma.Operation{OperationID: "delete-destination", Method: http.MethodDelete, Path: "/api/v1/destinations/{id}",
		Summary: "Delete a destination", Tags: tags, DefaultStatus: http.StatusNoContent}, a.deleteDestination)
	huma.Register(api, huma.Operation{OperationID: "test-destination", Method: http.MethodPost, Path: "/api/v1/destinations/{id}/test",
		Summary: "Test a stored destination", Tags: tags}, a.testStoredDestination)
}

// sameServer reports whether a configuration still points at the stored
// server and login, so the stored password may be reused for it. Moving to a
// weaker TLS mode counts as a different server.
func sameServer(cur store.Destination, host string, port int32, username, tlsMode string) bool {
	if !strings.EqualFold(cur.Host, host) || cur.Port != port || cur.Username != username {
		return false
	}
	rank := map[string]int{"none": 0, "starttls": 1, "implicit": 2}
	return rank[tlsMode] >= rank[cur.TlsMode]
}
