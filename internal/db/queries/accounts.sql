-- name: ListAccounts :many
SELECT * FROM accounts ORDER BY name;

-- name: GetAccount :one
SELECT * FROM accounts WHERE id = $1;

-- name: CreateAccount :one
INSERT INTO accounts (id, name, destination_id) VALUES ($1, $2, $3) RETURNING *;

-- name: UpdateAccount :one
UPDATE accounts SET name = $2, destination_id = $3, updated_at = now() WHERE id = $1 RETURNING *;

-- name: DeleteAccount :exec
DELETE FROM accounts WHERE id = $1;

-- name: SetAccountAuthorised :one
UPDATE accounts SET enc_refresh_token = $2, gmail_address = $3, status = 'ok', authorised_at = now(),
    last_connection_error = '', last_connection_check = now(), updated_at = now()
WHERE id = $1 RETURNING *;

-- name: SetAccountStatus :exec
UPDATE accounts SET status = $2, last_connection_error = $3, last_connection_check = now(), updated_at = now()
WHERE id = $1;

-- name: CountAccounts :one
SELECT count(*) FROM accounts;
