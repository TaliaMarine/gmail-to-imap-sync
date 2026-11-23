package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/mapping"
)

// LabelView is a Gmail label with what the current settings do with it.
type LabelView struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	System       bool       `json:"system"`
	Excluded     bool       `json:"excluded"`
	Folder       string     `json:"folder" doc:"Destination folder; empty when the label produces none"`
	CollidesWith []string   `json:"collides_with" doc:"Other label IDs that map to the same folder"`
	LastSeenAt   *time.Time `json:"last_seen_at,omitempty"`
}

type LabelsOutput struct {
	Body struct {
		Items []LabelView `json:"items"`
	}
}

type FolderPreviewInput struct {
	ID   string `path:"id"`
	Body SyncSettings
}

func labelViews(rows []store.AccountLabel, s mapping.Settings) []LabelView {
	labels := make([]mapping.Label, 0, len(rows))
	seen := map[string]pgtype.Timestamptz{}
	for _, r := range rows {
		labels = append(labels, mapping.Label{ID: r.LabelID, Name: r.Name, System: r.Type == "system"})
		seen[r.LabelID] = r.LastSeenAt
	}
	prev := mapping.Preview(labels, s)
	out := make([]LabelView, 0, len(prev))
	for _, p := range prev {
		c := p.CollidesWith
		if c == nil {
			c = []string{}
		}
		out = append(out, LabelView{
			ID: p.LabelID, Name: p.Name, System: p.System, Excluded: p.Excluded, Folder: p.Folder,
			CollidesWith: c, LastSeenAt: ts(seen[p.LabelID]),
		})
	}
	return out
}

func (a *API) accountLabelViews(ctx context.Context, acct store.Account, settings *SyncSettings) ([]LabelView, error) {
	rows, err := a.db.ListLabels(ctx, acct.ID)
	if err != nil {
		return nil, a.internalError(err, "list labels")
	}
	if settings == nil {
		s, err := a.db.GetSyncSettings(ctx, acct.ID)
		if err != nil {
			return nil, a.notFoundOr(err, "sync settings")
		}
		b, err := syncSettingsBody(s)
		if err != nil {
			return nil, a.internalError(err, "decode sync settings")
		}
		settings = &b
	}
	return labelViews(rows, settings.mappingSettings(a.destinationDelimiter(ctx, acct))), nil
}

func (a *API) listLabels(ctx context.Context, in *IDInput) (*LabelsOutput, error) {
	acct, err := a.loadAccount(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	views, err := a.accountLabelViews(ctx, acct, nil)
	if err != nil {
		return nil, err
	}
	out := &LabelsOutput{}
	out.Body.Items = views
	return out, nil
}

func (a *API) refreshLabels(ctx context.Context, in *IDInput) (*LabelsOutput, error) {
	acct, err := a.loadAccount(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	g, err := a.clients.Gmail(ctx, acct, nil)
	if err != nil {
		return nil, huma.Error409Conflict("Gmail is not connected for this account: " + shortError(err))
	}
	labels, err := g.Labels(ctx)
	if err != nil {
		return nil, huma.Error502BadGateway("Gmail could not be reached: " + shortError(err))
	}
	var started time.Time
	if err := a.queryRow(ctx, "SELECT now()").Scan(&started); err != nil {
		return nil, a.internalError(err, "read clock")
	}
	for _, l := range labels {
		typ := "user"
		if l.System {
			typ = "system"
		}
		if err := a.db.UpsertLabel(ctx, store.UpsertLabelParams{AccountID: acct.ID, LabelID: l.ID, Name: l.Name, Type: typ}); err != nil {
			return nil, a.internalError(err, "store label")
		}
	}
	// Labels Gmail no longer returns are dropped from the cache (messages keep
	// their copies; nothing is deleted at the destination).
	if err := a.db.DeleteLabelsNotSeenSince(ctx, store.DeleteLabelsNotSeenSinceParams{AccountID: acct.ID, LastSeenAt: tstz(started)}); err != nil {
		return nil, a.internalError(err, "prune labels")
	}
	return a.listLabels(ctx, in)
}

func (a *API) previewFolders(ctx context.Context, in *FolderPreviewInput) (*LabelsOutput, error) {
	acct, err := a.loadAccount(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	b := in.Body
	normaliseSyncSettings(&b)
	if err := b.validate(a.destinationDelimiter(ctx, acct)); err != nil {
		return nil, err
	}
	views, err := a.accountLabelViews(ctx, acct, &b)
	if err != nil {
		return nil, err
	}
	out := &LabelsOutput{}
	out.Body.Items = views
	return out, nil
}

func (a *API) registerLabels(api huma.API) {
	tags := []string{"Labels"}
	huma.Register(api, huma.Operation{OperationID: "list-labels", Method: http.MethodGet, Path: "/api/v1/accounts/{id}/labels",
		Summary: "List Gmail labels with their destination folders", Tags: tags}, a.listLabels)
	huma.Register(api, huma.Operation{OperationID: "refresh-labels", Method: http.MethodPost, Path: "/api/v1/accounts/{id}/labels/refresh",
		Summary: "Reload the labels from Gmail", Tags: tags}, a.refreshLabels)
	huma.Register(api, huma.Operation{OperationID: "preview-folders", Method: http.MethodPost, Path: "/api/v1/accounts/{id}/folder-preview",
		Summary:     "Preview the folders that candidate settings would produce",
		Description: "Does not save anything. Shows excluded labels and labels that collide on one folder.",
		Tags:        tags}, a.previewFolders)
}
