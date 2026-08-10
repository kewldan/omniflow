-- name: GetTelemetryInstallationID :one
SELECT installation_id FROM telemetry_installation WHERE singleton = true;

-- name: InsertOutboxEvent :one
INSERT INTO outbox_events (topic, payload) VALUES ($1, $2) RETURNING *;

-- name: GetRemnawaveUserIDByTelegramID :one
SELECT remnawave_id
FROM remnawave_users
WHERE telegram_id = $1;

-- name: LinkTelegramRemnawaveUser :one
WITH locked AS (
  SELECT pg_advisory_xact_lock(sqlc.arg(telegram_id)::bigint)
), updated AS (
  UPDATE remnawave_users
  SET remnawave_id = sqlc.arg(remnawave_id),
      reconciled_at = now()
  FROM locked
  WHERE telegram_id = sqlc.arg(telegram_id)
  RETURNING remnawave_id
), new_user AS (
  INSERT INTO users (status)
  SELECT 'active'
  FROM locked
  WHERE NOT EXISTS (SELECT 1 FROM updated)
  RETURNING id
), inserted AS (
  INSERT INTO remnawave_users (user_id, remnawave_id, telegram_id, reconciled_at)
  SELECT id, sqlc.arg(remnawave_id), sqlc.arg(telegram_id), now()
  FROM new_user
  RETURNING remnawave_id
)
SELECT remnawave_id FROM updated
UNION ALL
SELECT remnawave_id FROM inserted;
