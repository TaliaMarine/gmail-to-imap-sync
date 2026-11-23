-- name: UpsertLabel :exec
INSERT INTO account_labels (account_id, label_id, name, type, last_seen_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (account_id, label_id) DO UPDATE SET name = EXCLUDED.name, type = EXCLUDED.type, last_seen_at = now();

-- name: ListLabels :many
SELECT * FROM account_labels WHERE account_id = $1 ORDER BY type DESC, name;

-- name: DeleteLabelsNotSeenSince :exec
DELETE FROM account_labels WHERE account_id = $1 AND last_seen_at < $2;
