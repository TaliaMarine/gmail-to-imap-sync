package api

import (
	"time"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
)

// SessionSummary is one synchronisation session as the UI shows it.
type SessionSummary struct {
	ID                string     `json:"id" format:"uuid"`
	AccountID         string     `json:"account_id" format:"uuid"`
	Status            string     `json:"status" enum:"queued,running,completed,completed_with_errors,failed,cancelled,abandoned"`
	TriggerType       string     `json:"trigger_type" enum:"scheduled,manual,initial,retry,recovery"`
	SyncType          string     `json:"sync_type" enum:"initial,incremental,reconcile,retry"`
	Phase             string     `json:"phase" doc:"Current phase while running; empty otherwise"`
	CurrentFolder     string     `json:"current_folder" doc:"Folder being delivered to"`
	CreatedAt         time.Time  `json:"created_at"`
	StartedAt         *time.Time `json:"started_at,omitempty"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`
	DurationMs        *int64     `json:"duration_ms,omitempty"`
	Discovered        int64      `json:"discovered"`
	Processed         int64      `json:"processed"`
	Copied            int64      `json:"copied"`
	Skipped           int64      `json:"skipped"`
	Failed            int64      `json:"failed"`
	Recovered         int64      `json:"recovered"`
	FoldersCreated    int64      `json:"folders_created"`
	APIRequests       int64      `json:"api_requests"`
	BytesCopied       int64      `json:"bytes_copied"`
	StartingHistoryID *int64     `json:"starting_history_id,omitempty"`
	EndingHistoryID   *int64     `json:"ending_history_id,omitempty"`
	LastError         string     `json:"last_error"`
	CancelRequested   bool       `json:"cancel_requested"`
}

func sessionSummary(s store.SyncSession) SessionSummary {
	return SessionSummary{
		ID: s.ID.String(), AccountID: s.AccountID.String(),
		Status: s.Status, TriggerType: s.TriggerType, SyncType: s.SyncType,
		Phase: s.Phase, CurrentFolder: s.CurrentFolder,
		CreatedAt: tsOrZero(s.CreatedAt), StartedAt: ts(s.StartedAt), FinishedAt: ts(s.FinishedAt),
		DurationMs: int8p(s.DurationMs),
		Discovered: s.DiscoveredCount, Processed: s.ProcessedCount, Copied: s.CopiedCount,
		Skipped: s.SkippedCount, Failed: s.FailedCount, Recovered: s.RecoveredCount,
		FoldersCreated: s.FoldersCreatedCount, APIRequests: s.ApiRequestCount, BytesCopied: s.BytesCopied,
		StartingHistoryID: int8p(s.StartingHistoryID), EndingHistoryID: int8p(s.EndingHistoryID),
		LastError: s.LastError, CancelRequested: s.CancelRequestedAt.Valid,
	}
}

func sessionSummaries(rows []store.SyncSession) []SessionSummary {
	out := make([]SessionSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, sessionSummary(r))
	}
	return out
}

// AccountSummary is a Gmail account with its sync state; it never carries
// tokens.
type AccountSummary struct {
	ID                   string     `json:"id" format:"uuid"`
	Name                 string     `json:"name"`
	GmailAddress         string     `json:"gmail_address"`
	Status               string     `json:"status" enum:"unauthorised,ok,needs_reauth,disabled,error"`
	StatusError          string     `json:"status_error" doc:"Last connection error, if any"`
	LastConnectionCheck  *time.Time `json:"last_connection_check,omitempty"`
	AuthorisedAt         *time.Time `json:"authorised_at,omitempty" doc:"When Google access was last granted"`
	DestinationID        *string    `json:"destination_id,omitempty" format:"uuid"`
	DestinationName      string     `json:"destination_name"`
	SyncEnabled          bool       `json:"sync_enabled"`
	InitialSyncCompleted bool       `json:"initial_sync_completed"`
	LastSuccessfulSync   *time.Time `json:"last_successful_sync,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
}
