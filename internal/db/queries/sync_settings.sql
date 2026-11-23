-- name: GetSyncSettings :one
SELECT * FROM account_sync_settings WHERE account_id = $1;

-- name: EnsureSyncSettings :exec
INSERT INTO account_sync_settings (account_id) VALUES ($1) ON CONFLICT DO NOTHING;

-- name: UpdateSyncSettings :one
UPDATE account_sync_settings SET
    enabled = $2, schedule_interval_seconds = $3, initial_sync_after = $4,
    archive_folder = $5, sent_folder = $6, inbox_folder = $7,
    excluded_labels = $8, label_mapping = $9, flag_mapping = $10,
    subscribe_folders = $11, worker_count = $12, rate_fraction = $13,
    batch_size = $14, retry_max_attempts = $15, updated_at = now()
WHERE account_id = $1 RETURNING *;

-- name: ListScheduledAccounts :many
-- Accounts due for a scheduled incremental run: enabled, authorised, with an
-- enabled destination, and not run for longer than their interval.
SELECT a.id AS account_id, s.schedule_interval_seconds,
       (SELECT max(created_at) FROM sync_sessions ss WHERE ss.account_id = a.id AND ss.trigger_type IN ('scheduled', 'initial', 'manual'))::timestamptz AS last_scheduled
FROM accounts a
JOIN account_sync_settings s ON s.account_id = a.id
JOIN destinations d ON d.id = a.destination_id
WHERE s.enabled AND d.enabled AND a.status = 'ok';
