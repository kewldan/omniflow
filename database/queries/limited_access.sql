-- Limited grace access: a subscription that expired or ran out of traffic
-- kept alive on one squad with a small allowance for a few days.

-- name: SetLimitedAccessPolicy :one
UPDATE subscription_policy
SET limited_access_on_expiry = sqlc.arg(on_expiry),
    limited_access_on_traffic = sqlc.arg(on_traffic),
    limited_access_squad_id = sqlc.narg(squad_id),
    limited_access_traffic_gb = sqlc.narg(traffic_gb),
    limited_access_days = sqlc.narg(days),
    updated_at = now()
WHERE singleton
RETURNING *;

-- name: ListLimitedAccessCandidates :many
-- Entitlements that have just expired, or that Remnawave reports out of
-- traffic, and may open a limited access period now. Each must be its
-- subscription's latest live entitlement, on an active subscription, with no
-- period already open for the subscription and none opened before for the
-- same entitlement and trigger. Expiry counts from the remote expiry (the paid
-- end plus the plan's grace) and only within the last day, so turning the
-- switch on does not revive every subscription that ever lapsed.
SELECT e.id AS entitlement_id, e.user_id, e.subscription_id, candidate.trigger::text AS trigger
FROM entitlements e
JOIN subscriptions s ON s.id = e.subscription_id AND s.status = 'active'
JOIN plan_versions pv ON pv.id = e.plan_version_id
JOIN subscription_policy sp ON sp.singleton
CROSS JOIN LATERAL (
  SELECT 'expiry' AS trigger
  WHERE sp.limited_access_on_expiry
    AND e.status IN ('active', 'limited', 'expired')
    AND e.ends_at + make_interval(secs => pv.grace_period_seconds) <= now()
    AND e.ends_at + make_interval(secs => pv.grace_period_seconds) > now() - interval '1 day'
  UNION ALL
  SELECT 'traffic' AS trigger
  WHERE sp.limited_access_on_traffic
    AND e.status = 'limited'
    AND e.ends_at > now()
) candidate
WHERE sp.limited_access_squad_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM entitlements newer
    WHERE newer.subscription_id = e.subscription_id AND newer.id <> e.id
      AND newer.status NOT IN ('superseded', 'failed') AND newer.ends_at > e.ends_at
  )
  AND NOT EXISTS (
    SELECT 1 FROM limited_access_sessions open_session
    WHERE open_session.subscription_id = e.subscription_id AND open_session.status = 'active'
  )
  AND NOT EXISTS (
    SELECT 1 FROM limited_access_sessions earlier
    WHERE earlier.entitlement_id = e.id AND earlier.trigger = candidate.trigger
  )
ORDER BY e.ends_at
LIMIT sqlc.arg(row_limit);

-- name: OpenLimitedAccess :one
-- Opens a period with the installation's current squad, allowance and length.
-- A second pass racing for the same entitlement matches the unique keys and
-- inserts nothing.
INSERT INTO limited_access_sessions (user_id, subscription_id, entitlement_id, trigger, squad_id, traffic_bytes, ends_at)
SELECT sqlc.arg(user_id), sqlc.arg(subscription_id), sqlc.arg(entitlement_id), sqlc.arg(trigger),
       sp.limited_access_squad_id, sp.limited_access_traffic_gb::bigint * 1073741824,
       now() + make_interval(days => sp.limited_access_days)
FROM subscription_policy sp
WHERE sp.singleton AND sp.limited_access_squad_id IS NOT NULL
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetLimitedAccess :one
SELECT * FROM limited_access_sessions WHERE id = sqlc.arg(session_id);

-- name: GetActiveLimitedAccess :one
-- The open period that shapes what an entitlement's operations push.
SELECT * FROM limited_access_sessions
WHERE entitlement_id = sqlc.arg(entitlement_id) AND status = 'active' AND ends_at > now()
LIMIT 1;

-- name: EndLimitedAccess :exec
UPDATE limited_access_sessions SET status = 'ended', ended_at = now()
WHERE id = sqlc.arg(session_id) AND status = 'active';

-- name: RecordLimitedAccessBase :exec
UPDATE limited_access_sessions SET base_used_bytes = sqlc.arg(base_used_bytes)
WHERE id = sqlc.arg(session_id) AND base_used_bytes IS NULL;

-- name: RestoreLimitedAccess :many
-- A payment on the subscription restores the full plan.
UPDATE limited_access_sessions SET status = 'restored', ended_at = now()
WHERE subscription_id = sqlc.arg(subscription_id) AND status = 'active'
RETURNING *;

-- name: EndStaleLimitedAccess :many
-- Periods that ran out, or whose entitlement a renewal, a gift or a code
-- retired, and that no operation ended.
UPDATE limited_access_sessions las SET status = 'ended', ended_at = now()
FROM entitlements e
WHERE e.id = las.entitlement_id AND las.status = 'active'
  AND (las.ends_at <= now() OR e.status = 'superseded')
RETURNING las.*;
