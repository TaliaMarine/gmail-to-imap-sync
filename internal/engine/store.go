package engine

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/TaliaMarine/gmail-to-imap-sync/internal/db/store"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/gmail"
	"github.com/TaliaMarine/gmail-to-imap-sync/internal/imapdest"
)

// Store is the slice of the database the engine uses. *store.Queries (and so
// *db.DB) implements it; tests use an in-memory fake.
type Store interface {
	GetAccount(ctx context.Context, id uuid.UUID) (store.Account, error)
	GetSyncSettings(ctx context.Context, accountID uuid.UUID) (store.AccountSyncSetting, error)
	GetSyncState(ctx context.Context, accountID uuid.UUID) (store.SyncState, error)
	EnsureSyncState(ctx context.Context, accountID uuid.UUID) error
	BeginInitialSync(ctx context.Context, arg store.BeginInitialSyncParams) error
	SaveInitialCursor(ctx context.Context, arg store.SaveInitialCursorParams) error
	ResetInitialSync(ctx context.Context, accountID uuid.UUID) error
	CompleteInitialSync(ctx context.Context, accountID uuid.UUID) error
	SaveCheckpoint(ctx context.Context, arg store.SaveCheckpointParams) error
	SaveFinalCheckpoint(ctx context.Context, arg store.SaveFinalCheckpointParams) error
	TouchLastSuccess(ctx context.Context, accountID uuid.UUID) error
	SetLearnedRate(ctx context.Context, arg store.SetLearnedRateParams) error
	UpsertLabel(ctx context.Context, arg store.UpsertLabelParams) error
	SetAccountStatus(ctx context.Context, arg store.SetAccountStatusParams) error

	HeartbeatSession(ctx context.Context, arg store.HeartbeatSessionParams) (pgtype.Timestamptz, error)
	SetSessionStartingHistory(ctx context.Context, arg store.SetSessionStartingHistoryParams) error
	SetSessionType(ctx context.Context, arg store.SetSessionTypeParams) error
	FinishSession(ctx context.Context, arg store.FinishSessionParams) (store.SyncSession, error)
	InsertEvent(ctx context.Context, arg store.InsertEventParams) error

	UpsertFailure(ctx context.Context, arg store.UpsertFailureParams) (store.FailedMessage, error)
	GetFailureByMessage(ctx context.Context, arg store.GetFailureByMessageParams) (store.FailedMessage, error)
	PendingFailureKeys(ctx context.Context, accountID uuid.UUID) ([]store.PendingFailureKeysRow, error)
	DueFailures(ctx context.Context, arg store.DueFailuresParams) ([]store.FailedMessage, error)
	FailuresByIDs(ctx context.Context, ids []uuid.UUID) ([]store.FailedMessage, error)
	ResolveFailure(ctx context.Context, id uuid.UUID) error
	SetFailureStatus(ctx context.Context, arg store.SetFailureStatusParams) (store.FailedMessage, error)
}

// Dest is the destination mailbox. *imapdest.Session implements it.
type Dest interface {
	Deliver(ctx context.Context, m imapdest.Message, folders []string) []imapdest.Result
	Delimiter() string
	Stats() imapdest.Stats
}

var (
	_ Store        = (*store.Queries)(nil)
	_ Dest         = (*imapdest.Session)(nil)
	_ gmail.Source = (*gmail.Client)(nil)
)
