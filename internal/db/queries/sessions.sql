-- name: CreateSession :one
INSERT INTO sync_sessions (id, account_id, status, trigger_type, sync_type, params, run_at)
VALUES ($1, $2, 'queued', $3, $4, $5, COALESCE(sqlc.narg('run_at')::timestamptz, now()))
RETURNING *;

-- name: ClaimSession :one
-- Takes the oldest due queued session and leases it to a worker.
UPDATE sync_sessions SET status = 'running', started_at = COALESCE(started_at, now()),
    worker_id = sqlc.arg('worker_id'), heartbeat_at = now(), lease_until = now() + make_interval(secs => sqlc.arg('lease_secs')::int)
WHERE id = (
    SELECT id FROM sync_sessions WHERE status = 'queued' AND run_at <= now()
    ORDER BY run_at, created_at FOR UPDATE SKIP LOCKED LIMIT 1
)
RETURNING *;

-- name: HeartbeatSession :one
-- Extends the lease and stores live progress. Returns cancel_requested_at so
-- the runner learns about cancellation without a second query. No row means
-- the lease was lost (another worker took over).
UPDATE sync_sessions SET heartbeat_at = now(), lease_until = now() + make_interval(secs => sqlc.arg('lease_secs')::int),
    phase = sqlc.arg('phase'), current_folder = sqlc.arg('current_folder'),
    discovered_count = sqlc.arg('discovered'), processed_count = sqlc.arg('processed'),
    copied_count = sqlc.arg('copied'), skipped_count = sqlc.arg('skipped'),
    failed_count = sqlc.arg('failed'), recovered_count = sqlc.arg('recovered'),
    folders_created_count = sqlc.arg('folders_created'),
    api_request_count = sqlc.arg('api_requests'), bytes_copied = sqlc.arg('bytes_copied')
WHERE id = sqlc.arg('id') AND worker_id = sqlc.arg('worker_id') AND status = 'running'
RETURNING cancel_requested_at;

-- name: SetSessionStartingHistory :exec
UPDATE sync_sessions SET starting_history_id = $2 WHERE id = $1;

-- name: FinishSession :one
UPDATE sync_sessions SET status = sqlc.arg('status'), finished_at = now(), lease_until = NULL, phase = '',
    current_folder = '', ending_history_id = sqlc.arg('ending_history_id'), last_error = sqlc.arg('last_error'),
    discovered_count = sqlc.arg('discovered'), processed_count = sqlc.arg('processed'),
    copied_count = sqlc.arg('copied'), skipped_count = sqlc.arg('skipped'),
    failed_count = sqlc.arg('failed'), recovered_count = sqlc.arg('recovered'),
    folders_created_count = sqlc.arg('folders_created'),
    api_request_count = sqlc.arg('api_requests'), bytes_copied = sqlc.arg('bytes_copied'),
    duration_ms = (extract(epoch FROM (now() - COALESCE(started_at, created_at))) * 1000)::bigint
WHERE id = sqlc.arg('id') AND status = 'running'
RETURNING *;

-- name: RequestSessionCancel :one
-- A queued session is cancelled at once; a running one at its next check.
UPDATE sync_sessions SET cancel_requested_at = now(),
    status = CASE WHEN status = 'queued' THEN 'cancelled' ELSE status END,
    finished_at = CASE WHEN status = 'queued' THEN now() ELSE finished_at END
WHERE id = $1 AND status IN ('queued', 'running')
RETURNING *;

-- name: AbandonExpiredSessions :many
-- Running sessions whose lease lapsed belong to a dead worker.
UPDATE sync_sessions SET status = 'abandoned', finished_at = now(), lease_until = NULL,
    last_error = 'worker stopped responding'
WHERE status = 'running' AND lease_until < now()
RETURNING *;

-- name: GetSession :one
SELECT * FROM sync_sessions WHERE id = $1;

-- name: GetActiveSession :one
SELECT * FROM sync_sessions WHERE account_id = $1 AND status IN ('queued', 'running');

-- name: ListSessions :many
SELECT * FROM sync_sessions
WHERE (sqlc.narg('account_id')::uuid IS NULL OR account_id = sqlc.narg('account_id'))
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('trigger_type')::text IS NULL OR trigger_type = sqlc.narg('trigger_type'))
  AND (sqlc.narg('sync_type')::text IS NULL OR sync_type = sqlc.narg('sync_type'))
  AND (sqlc.narg('from')::timestamptz IS NULL OR created_at >= sqlc.narg('from'))
  AND (sqlc.narg('to')::timestamptz IS NULL OR created_at < sqlc.narg('to'))
  AND (sqlc.narg('before_created')::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg('before_created')::timestamptz, sqlc.narg('before_id')::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('lim');

-- name: LastFinishedSession :one
SELECT * FROM sync_sessions
WHERE account_id = $1 AND status IN ('completed', 'completed_with_errors')
ORDER BY finished_at DESC LIMIT 1;

-- name: SessionActivityByDay :many
SELECT date_trunc('day', created_at)::timestamptz AS day,
       sum(copied_count)::bigint AS copied, sum(skipped_count)::bigint AS skipped,
       sum(failed_count)::bigint AS failed, count(*)::bigint AS sessions
FROM sync_sessions
WHERE created_at >= $1 AND status NOT IN ('queued', 'running')
GROUP BY 1 ORDER BY 1;

-- name: PruneSessions :execrows
DELETE FROM sync_sessions WHERE created_at < $1 AND status NOT IN ('queued', 'running');

-- name: InsertEvent :exec
INSERT INTO sync_session_events (session_id, event_type, severity, message, metadata)
VALUES ($1, $2, $3, $4, $5);

-- name: ListEvents :many
SELECT * FROM sync_session_events WHERE session_id = $1 AND id > $2 ORDER BY id LIMIT $3;

-- name: PruneEvents :execrows
DELETE FROM sync_session_events WHERE created_at < $1;

-- name: SetSessionType :exec
UPDATE sync_sessions SET sync_type = $2 WHERE id = $1;

-- name: CountRecentAbandoned :one
SELECT count(*) FROM sync_sessions WHERE account_id = $1 AND status = 'abandoned' AND created_at >= $2;

-- name: RecentSessionStatuses :many
SELECT status FROM sync_sessions WHERE account_id = $1 AND status NOT IN ('queued', 'running')
ORDER BY created_at DESC LIMIT $2;

-- name: SessionGauges :one
SELECT
    count(*) FILTER (WHERE status = 'queued')::bigint AS queued,
    count(*) FILTER (WHERE status = 'running')::bigint AS running
FROM sync_sessions WHERE status IN ('queued', 'running');

-- name: LastSuccessByAccount :many
SELECT account_id, max(finished_at)::timestamptz AS finished_at FROM sync_sessions
WHERE status IN ('completed', 'completed_with_errors') GROUP BY account_id;

-- name: CountAccountsByStatus :many
SELECT status, count(*)::bigint AS n FROM accounts GROUP BY status;
