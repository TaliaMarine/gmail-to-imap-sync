-- name: GetAppSettings :one
SELECT * FROM app_settings WHERE id;

-- name: UpdateAppSettings :one
UPDATE app_settings SET
    oauth_client_id = $1,
    enc_oauth_client_secret = $2,
    session_retention_days = $3,
    event_retention_days = $4,
    enc_notification_webhook = $5,
    updated_at = now()
WHERE id
RETURNING *;
