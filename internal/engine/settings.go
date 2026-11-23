package engine

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/mapping"
)

// flagMapping is the JSON stored in account_sync_settings.flag_mapping.
type flagMapping struct {
	UnreadAsSeen     bool     `json:"unread_as_seen"`
	StarredAsFlagged bool     `json:"starred_as_flagged"`
	FolderLabels     []string `json:"folder_labels"`
}

// MappingSettings converts the stored settings row into the rules the
// mapping package evaluates. delimiter is the destination's hierarchy
// delimiter.
func MappingSettings(row store.AccountSyncSetting, delimiter string) (mapping.Settings, error) {
	s := mapping.Settings{
		InboxFolder:   row.InboxFolder,
		SentFolder:    row.SentFolder,
		ArchiveFolder: row.ArchiveFolder,
		Delimiter:     delimiter,
	}
	if err := unmarshalOrEmpty(row.ExcludedLabels, &s.Excluded); err != nil {
		return s, fmt.Errorf("excluded_labels: %w", err)
	}
	if err := unmarshalOrEmpty(row.LabelMapping, &s.LabelMapping); err != nil {
		return s, fmt.Errorf("label_mapping: %w", err)
	}
	var fm flagMapping
	if err := unmarshalOrEmpty(row.FlagMapping, &fm); err != nil {
		return s, fmt.Errorf("flag_mapping: %w", err)
	}
	s.UnreadAsSeen, s.StarredAsFlagged, s.FolderLabels = fm.UnreadAsSeen, fm.StarredAsFlagged, fm.FolderLabels
	if err := s.Validate(); err != nil {
		return s, fmt.Errorf("invalid sync settings: %w", err)
	}
	return s, nil
}

func unmarshalOrEmpty(b []byte, v any) error {
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

// Params are the optional parameters of a session, stored as JSON.
type Params struct {
	After      string   `json:"after,omitempty"`       // reconcile: YYYY-MM-DD
	LabelID    string   `json:"label_id,omitempty"`    // reconcile: only this label
	FailureIDs []string `json:"failure_ids,omitempty"` // retry: the failures to process
}

func parseParams(b []byte) (Params, error) {
	var p Params
	if len(b) == 0 {
		return p, nil
	}
	err := json.Unmarshal(b, &p)
	return p, err
}

// gmailQuery builds the list query for messages after a date.
func gmailQuery(after time.Time) string {
	if after.IsZero() {
		return ""
	}
	return "after:" + after.Format("2006/01/02")
}
