-- Temporary squad access: one squad added for some hours.

-- name: InsertSquadAccessGrant :one
INSERT INTO squad_access_grants (user_id, subscription_id, squad_id, source, granted_by, order_id, reason, idempotency_key, ends_at)
VALUES (sqlc.arg(user_id), sqlc.arg(subscription_id), sqlc.arg(squad_id), sqlc.arg(source), sqlc.narg(granted_by),
        sqlc.narg(order_id), sqlc.narg(reason), sqlc.arg(idempotency_key), now() + make_interval(hours => sqlc.arg(hours)::int))
ON CONFLICT (idempotency_key) DO NOTHING
RETURNING *;

-- name: GetSquadAccessGrantByKey :one
SELECT * FROM squad_access_grants WHERE idempotency_key = sqlc.arg(idempotency_key);

-- name: GetSquadAccessGrant :one
SELECT * FROM squad_access_grants WHERE id = sqlc.arg(grant_id);

-- name: ListActiveSquadGrants :many
-- The temporary squads in force on a subscription now.
SELECT * FROM squad_access_grants
WHERE subscription_id = sqlc.arg(subscription_id) AND status = 'active' AND ends_at > now()
ORDER BY starts_at;

-- name: ListSquadGrantsForSubscription :many
SELECT * FROM squad_access_grants
WHERE subscription_id = sqlc.arg(subscription_id)
ORDER BY created_at DESC
LIMIT 20;

-- name: EndSquadAccessGrant :exec
UPDATE squad_access_grants SET status = 'ended', ended_at = now()
WHERE id = sqlc.arg(grant_id) AND status = 'active';

-- name: RevokeSquadAccessGrant :one
-- An operator takes a grant back before its hours are up.
UPDATE squad_access_grants SET status = 'revoked', ended_at = now()
WHERE id = sqlc.arg(grant_id) AND subscription_id = sqlc.arg(subscription_id) AND status = 'active'
RETURNING *;

-- name: EndExpiredSquadGrants :many
-- Grants whose hours are up and that no scheduled operation ended — the
-- entitlement it was queued for was retired in between.
UPDATE squad_access_grants SET status = 'ended', ended_at = now()
WHERE status = 'active' AND ends_at <= now()
RETURNING *;
