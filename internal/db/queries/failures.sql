-- name: UpsertFailure :one
-- One row per (account, message): a repeated failure replaces the details and
-- adds to the history, it never creates a second row. An ignored failure
-- stays ignored.
INSERT INTO failed_messages (id, account_id, gmail_message_id, gmail_thread_id, subject, sender, recipients,
    message_date, intended_folders, completed_folders, failed_folders, error_category, error_message,
    error_history, status, retry_exhausted, next_retry_at, last_session_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 'pending', $15, $16, $17)
ON CONFLICT (account_id, gmail_message_id) DO UPDATE SET
    gmail_thread_id = EXCLUDED.gmail_thread_id,
    subject = EXCLUDED.subject, sender = EXCLUDED.sender, recipients = EXCLUDED.recipients,
    message_date = EXCLUDED.message_date,
    intended_folders = EXCLUDED.intended_folders,
    completed_folders = EXCLUDED.completed_folders,
    failed_folders = EXCLUDED.failed_folders,
    error_category = EXCLUDED.error_category, error_message = EXCLUDED.error_message,
    error_history = EXCLUDED.error_history, -- the caller merges and caps the history
    attempt_count = failed_messages.attempt_count + 1,
    status = CASE WHEN failed_messages.status = 'ignored' THEN 'ignored' ELSE 'pending' END,
    retry_exhausted = EXCLUDED.retry_exhausted, retry_requested = false,
    next_retry_at = EXCLUDED.next_retry_at,
    last_failed_at = now(), last_attempted_at = now(),
    last_session_id = EXCLUDED.last_session_id, updated_at = now()
RETURNING *;

-- name: GetFailure :one
SELECT * FROM failed_messages WHERE id = $1;

-- name: GetFailureByMessage :one
SELECT * FROM failed_messages WHERE account_id = $1 AND gmail_message_id = $2;

-- name: ListFailures :many
SELECT * FROM failed_messages
WHERE (sqlc.narg('account_id')::uuid IS NULL OR account_id = sqlc.narg('account_id'))
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('category')::text IS NULL OR error_category = sqlc.narg('category'))
  AND (sqlc.narg('search')::text IS NULL OR subject ILIKE '%' || sqlc.narg('search') || '%' OR sender ILIKE '%' || sqlc.narg('search') || '%')
  AND (sqlc.narg('before_failed')::timestamptz IS NULL
       OR (last_failed_at, id) < (sqlc.narg('before_failed')::timestamptz, sqlc.narg('before_id')::uuid))
ORDER BY last_failed_at DESC, id DESC
LIMIT sqlc.arg('lim');

-- name: DueFailures :many
-- Retryable failures whose backoff has passed, oldest first.
SELECT * FROM failed_messages
WHERE account_id = $1 AND status = 'pending' AND NOT retry_exhausted
  AND (retry_requested OR error_category NOT IN ('gmail_auth', 'imap_auth', 'config', 'parse', 'gmail_message_unavailable'))
  AND (next_retry_at IS NULL OR next_retry_at <= now())
ORDER BY first_failed_at LIMIT $2;

-- name: FailuresByIDs :many
SELECT * FROM failed_messages WHERE id = ANY($1::uuid[]);

-- name: PendingFailureIDs :many
SELECT id FROM failed_messages
WHERE status = 'pending' AND (sqlc.narg('account_id')::uuid IS NULL OR account_id = sqlc.narg('account_id'));

-- name: ResolveFailure :exec
DELETE FROM failed_messages WHERE id = $1;

-- name: SetFailureStatus :one
UPDATE failed_messages SET status = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: ClearIgnoredFailures :execrows
DELETE FROM failed_messages WHERE status = 'ignored' AND (sqlc.narg('account_id')::uuid IS NULL OR account_id = sqlc.narg('account_id'));

-- name: CountFailures :many
SELECT account_id, status, count(*)::bigint AS n FROM failed_messages GROUP BY account_id, status;

-- name: ResetFailureForRetry :exec
-- Manual retry: allow another automatic run and clear the exhausted flag.
UPDATE failed_messages SET status = 'pending', retry_exhausted = false, retry_requested = true, next_retry_at = NULL, updated_at = now() WHERE id = $1;

-- name: PendingFailureKeys :many
-- Pending failures of an account, so a message delivered by any route can
-- resolve its failure row.
SELECT id, gmail_message_id FROM failed_messages WHERE account_id = $1 AND status = 'pending' LIMIT 100000;

-- name: DeleteFailuresForAccount :exec
DELETE FROM failed_messages WHERE account_id = $1;
