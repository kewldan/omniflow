-- name: GetTelegramAvatar :one
SELECT * FROM telegram_avatars WHERE telegram_id = $1;

-- name: UpsertTelegramAvatar :exec
INSERT INTO telegram_avatars (telegram_id, photo, fetched_at)
VALUES (sqlc.arg(telegram_id), sqlc.narg(photo), now())
ON CONFLICT (telegram_id) DO UPDATE
SET photo = EXCLUDED.photo, fetched_at = EXCLUDED.fetched_at;
