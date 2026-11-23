-- name: GetSyncState :one
SELECT * FROM sync_state WHERE account_id = $1;

-- name: EnsureSyncState :exec
INSERT INTO sync_state (account_id) VALUES ($1) ON CONFLICT DO NOTHING;

-- name: BeginInitialSync :exec
-- Records the history ID taken before enumeration starts; a restart that
-- finds one keeps it (the list may have been partly processed already).
UPDATE sync_state SET
    start_history_at = CASE WHEN start_history_id IS NULL THEN now() ELSE start_history_at END,
    start_history_id = COALESCE(start_history_id, $2), updated_at = now()
WHERE account_id = $1;

-- name: SaveInitialCursor :exec
UPDATE sync_state SET initial_sync_cursor = $2, updated_at = now() WHERE account_id = $1;

-- name: ResetInitialSync :exec
UPDATE sync_state SET initial_sync_cursor = NULL, start_history_id = NULL, start_history_at = NULL,
    initial_sync_completed = false, last_successful_history_id = NULL, last_successful_sync_at = NULL,
    history_checkpoint_at = NULL, updated_at = now()
WHERE account_id = $1;

-- name: CompleteInitialSync :exec
UPDATE sync_state SET initial_sync_completed = true, initial_sync_cursor = NULL,
    last_successful_history_id = start_history_id, history_checkpoint_at = start_history_at,
    last_successful_sync_at = now(), updated_at = now()
WHERE account_id = $1;

-- name: SaveCheckpoint :exec
UPDATE sync_state SET last_successful_history_id = $2, last_successful_sync_at = now(), updated_at = now()
WHERE account_id = $1 AND (last_successful_history_id IS NULL OR last_successful_history_id <= $2);

-- name: SaveFinalCheckpoint :exec
-- The checkpoint after the last page: also records WHEN that history ID was
-- read (@at, taken before reading), the cutoff a reconcile would need.
UPDATE sync_state SET last_successful_history_id = sqlc.arg('history_id'), history_checkpoint_at = sqlc.arg('at'),
    last_successful_sync_at = now(), updated_at = now()
WHERE account_id = sqlc.arg('account_id') AND (last_successful_history_id IS NULL OR last_successful_history_id <= sqlc.arg('history_id'));

-- name: TouchLastSuccess :exec
UPDATE sync_state SET last_successful_sync_at = now(), updated_at = now() WHERE account_id = $1;

-- name: SetLearnedRate :exec
UPDATE sync_state SET learned_rate_fraction = $2, updated_at = now() WHERE account_id = $1;
