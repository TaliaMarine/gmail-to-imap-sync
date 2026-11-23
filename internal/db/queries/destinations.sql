-- name: ListDestinations :many
SELECT * FROM destinations ORDER BY name;

-- name: GetDestination :one
SELECT * FROM destinations WHERE id = $1;

-- name: CreateDestination :one
INSERT INTO destinations (id, name, host, port, tls_mode, tls_skip_verify, username, enc_password, enabled)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING *;

-- name: UpdateDestination :one
UPDATE destinations SET name = $2, host = $3, port = $4, tls_mode = $5, tls_skip_verify = $6,
    username = $7, enc_password = $8, enabled = $9, updated_at = now()
WHERE id = $1 RETURNING *;

-- name: DeleteDestination :exec
DELETE FROM destinations WHERE id = $1;

-- name: SetDestinationDelimiter :exec
UPDATE destinations SET hierarchy_delimiter = $2, updated_at = now() WHERE id = $1;
