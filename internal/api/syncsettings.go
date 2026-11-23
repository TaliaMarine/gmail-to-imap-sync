package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/mapping"
)

// FlagMapping says which Gmail state becomes IMAP flags or extra folders.
type FlagMapping struct {
	UnreadAsSeen     bool     `json:"unread_as_seen" doc:"A message without the UNREAD label is flagged \\Seen"`
	StarredAsFlagged bool     `json:"starred_as_flagged" doc:"STARRED becomes \\Flagged"`
	FolderLabels     []string `json:"folder_labels" doc:"System label IDs (STARRED, IMPORTANT, CATEGORY_*) that also get a folder"`
}

// SyncSettings configure how one account is synchronised. Changing them never
// deletes anything; they only affect future deliveries.
type SyncSettings struct {
	Enabled                 bool              `json:"enabled" doc:"Run scheduled synchronisation"`
	ScheduleIntervalSeconds int32             `json:"schedule_interval_seconds" minimum:"300" maximum:"2592000"`
	InitialSyncAfter        *string           `json:"initial_sync_after,omitempty" format:"date" doc:"Only import messages received on or after this date (first sync)"`
	ArchiveFolder           string            `json:"archive_folder" minLength:"1" maxLength:"255"`
	SentFolder              string            `json:"sent_folder" minLength:"1" maxLength:"255"`
	InboxFolder             string            `json:"inbox_folder" minLength:"1" maxLength:"255"`
	ExcludedLabels          []mapping.Matcher `json:"excluded_labels"`
	LabelMapping            map[string]string `json:"label_mapping" doc:"Gmail label ID to destination folder"`
	FlagMapping             FlagMapping       `json:"flag_mapping"`
	SubscribeFolders        bool              `json:"subscribe_folders"`
	WorkerCount             int32             `json:"worker_count" minimum:"1" maximum:"64"`
	RateFraction            float32           `json:"rate_fraction" minimum:"0.05" maximum:"1" doc:"Share of Gmail's per-user quota to use"`
	BatchSize               int32             `json:"batch_size" minimum:"1" maximum:"500"`
	RetryMaxAttempts        int32             `json:"retry_max_attempts" minimum:"0" maximum:"50"`
}

type SyncSettingsOutput struct{ Body SyncSettings }
type SyncSettingsUpdateInput struct {
	ID   string `path:"id"`
	Body SyncSettings
}

func syncSettingsBody(s store.AccountSyncSetting) (SyncSettings, error) {
	out := SyncSettings{
		Enabled: s.Enabled, ScheduleIntervalSeconds: s.ScheduleIntervalSeconds,
		ArchiveFolder: s.ArchiveFolder, SentFolder: s.SentFolder, InboxFolder: s.InboxFolder,
		SubscribeFolders: s.SubscribeFolders, WorkerCount: s.WorkerCount, RateFraction: s.RateFraction,
		BatchSize: s.BatchSize, RetryMaxAttempts: s.RetryMaxAttempts,
	}
	if s.InitialSyncAfter.Valid {
		d := s.InitialSyncAfter.Time.Format(time.DateOnly)
		out.InitialSyncAfter = &d
	}
	if err := json.Unmarshal(s.ExcludedLabels, &out.ExcludedLabels); err != nil {
		return out, err
	}
	if err := json.Unmarshal(s.LabelMapping, &out.LabelMapping); err != nil {
		return out, err
	}
	if err := json.Unmarshal(s.FlagMapping, &out.FlagMapping); err != nil {
		return out, err
	}
	normaliseSyncSettings(&out)
	return out, nil
}

// normaliseSyncSettings turns nil collections into empty ones so the JSON
// always has arrays and objects.
func normaliseSyncSettings(s *SyncSettings) {
	if s.ExcludedLabels == nil {
		s.ExcludedLabels = []mapping.Matcher{}
	}
	if s.LabelMapping == nil {
		s.LabelMapping = map[string]string{}
	}
	if s.FlagMapping.FolderLabels == nil {
		s.FlagMapping.FolderLabels = []string{}
	}
}

// mappingSettings converts the stored settings to the mapping rules; delim is
// the destination's hierarchy delimiter ("" means "/").
func (s SyncSettings) mappingSettings(delim string) mapping.Settings {
	return mapping.Settings{
		InboxFolder: s.InboxFolder, SentFolder: s.SentFolder, ArchiveFolder: s.ArchiveFolder,
		Excluded: s.ExcludedLabels, LabelMapping: s.LabelMapping, FolderLabels: s.FlagMapping.FolderLabels,
		UnreadAsSeen: s.FlagMapping.UnreadAsSeen, StarredAsFlagged: s.FlagMapping.StarredAsFlagged,
		Delimiter: delim,
	}
}

// validate checks cross-field rules the schema cannot express.
func (s SyncSettings) validate(delim string) error {
	if s.InitialSyncAfter != nil {
		if _, err := time.Parse(time.DateOnly, *s.InitialSyncAfter); err != nil {
			return huma.Error422UnprocessableEntity("initial_sync_after must be a date like 2024-01-31")
		}
	}
	if err := s.mappingSettings(delim).Validate(); err != nil {
		return huma.Error422UnprocessableEntity(err.Error())
	}
	return nil
}

// destinationDelimiter returns the hierarchy delimiter of the account's destination.
func (a *API) destinationDelimiter(ctx context.Context, acct store.Account) string {
	if acct.DestinationID.Valid {
		if d, err := a.db.GetDestination(ctx, acct.DestinationID.UUID); err == nil {
			return d.HierarchyDelimiter
		}
	}
	return ""
}

func (a *API) getSyncSettings(ctx context.Context, in *IDInput) (*SyncSettingsOutput, error) {
	acct, err := a.loadAccount(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	s, err := a.db.GetSyncSettings(ctx, acct.ID)
	if err != nil {
		return nil, a.notFoundOr(err, "sync settings")
	}
	body, err := syncSettingsBody(s)
	if err != nil {
		return nil, a.internalError(err, "decode sync settings")
	}
	return &SyncSettingsOutput{Body: body}, nil
}

func (a *API) updateSyncSettings(ctx context.Context, in *SyncSettingsUpdateInput) (*SyncSettingsOutput, error) {
	acct, err := a.loadAccount(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	b := in.Body
	normaliseSyncSettings(&b)
	if err := b.validate(a.destinationDelimiter(ctx, acct)); err != nil {
		return nil, err
	}
	excl, _ := json.Marshal(b.ExcludedLabels)
	lm, _ := json.Marshal(b.LabelMapping)
	fm, _ := json.Marshal(b.FlagMapping)
	p := store.UpdateSyncSettingsParams{
		AccountID: acct.ID, Enabled: b.Enabled, ScheduleIntervalSeconds: b.ScheduleIntervalSeconds,
		ArchiveFolder: b.ArchiveFolder, SentFolder: b.SentFolder, InboxFolder: b.InboxFolder,
		ExcludedLabels: excl, LabelMapping: lm, FlagMapping: fm,
		SubscribeFolders: b.SubscribeFolders, WorkerCount: b.WorkerCount, RateFraction: b.RateFraction,
		BatchSize: b.BatchSize, RetryMaxAttempts: b.RetryMaxAttempts,
	}
	if b.InitialSyncAfter != nil {
		t, _ := time.Parse(time.DateOnly, *b.InitialSyncAfter)
		p.InitialSyncAfter = pgtype.Date{Time: t, Valid: true}
	}
	s, err := a.db.UpdateSyncSettings(ctx, p)
	if err != nil {
		return nil, a.notFoundOr(err, "sync settings")
	}
	body, err := syncSettingsBody(s)
	if err != nil {
		return nil, a.internalError(err, "decode sync settings")
	}
	return &SyncSettingsOutput{Body: body}, nil
}

func (a *API) registerSyncSettings(api huma.API) {
	tags := []string{"Accounts"}
	huma.Register(api, huma.Operation{OperationID: "get-sync-settings", Method: http.MethodGet, Path: "/api/v1/accounts/{id}/sync-settings",
		Summary: "Get the synchronisation settings of an account", Tags: tags}, a.getSyncSettings)
	huma.Register(api, huma.Operation{OperationID: "update-sync-settings", Method: http.MethodPut, Path: "/api/v1/accounts/{id}/sync-settings",
		Summary: "Update the synchronisation settings of an account", Tags: tags}, a.updateSyncSettings)
}
