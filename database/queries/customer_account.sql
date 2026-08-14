-- The customer web panel's own read model for v0.9.
--
-- Every statement is scoped by `user_id`. Ownership is part of the query rather
-- than a check the caller is trusted to perform first, so an identifier lifted
-- from a URL can never address somebody else's subscription.

-- name: ListAccountSubscriptions :many
-- One row per active subscription, with the entitlement currently in force and
-- the plan version it was bought under.
--
-- The lateral join takes the latest entitlement that has not been superseded:
-- an extension writes a new row rather than editing the old one, so "current"
-- is the newest live row rather than the only row.
SELECT s.id, s.slot, s.label, s.remnawave_user_id,
       COALESCE(l.name, p.code, '') AS plan_name,
       COALESCE(p.code, '') AS plan_code,
       COALESCE(e.status, '') AS entitlement_status,
       e.ends_at,
       -- A pause freezes the remaining time, so the customer surfaces measure
       -- "days left" from this instant rather than from now. Without it a paused
       -- subscription counts down to zero on the customer's own screen while
       -- nothing is being consumed.
       e.paused_at,
       COALESCE(v.grace_period_seconds, 0)::bigint AS grace_period_seconds,
       v.traffic_allowance_bytes,
       v.device_limit,
       v.billing_period
FROM subscriptions s
LEFT JOIN LATERAL (
  SELECT * FROM entitlements ent
  WHERE ent.subscription_id = s.id AND ent.status <> 'superseded'
  ORDER BY ent.ends_at DESC LIMIT 1
) e ON true
LEFT JOIN plan_versions v ON v.id = e.plan_version_id
LEFT JOIN plans p ON p.id = v.plan_id
LEFT JOIN plan_localizations l ON l.plan_id = p.id AND l.locale = sqlc.arg(locale)
WHERE s.user_id = sqlc.arg(user_id) AND s.status = 'active'
ORDER BY s.slot;

-- name: GetAccountSubscription :one
SELECT s.id, s.slot, s.label, s.remnawave_user_id,
       COALESCE(l.name, p.code, '') AS plan_name,
       COALESCE(p.code, '') AS plan_code,
       COALESCE(e.status, '') AS entitlement_status,
       e.ends_at,
       -- A pause freezes the remaining time, so the customer surfaces measure
       -- "days left" from this instant rather than from now. Without it a paused
       -- subscription counts down to zero on the customer's own screen while
       -- nothing is being consumed.
       e.paused_at,
       COALESCE(v.grace_period_seconds, 0)::bigint AS grace_period_seconds,
       v.traffic_allowance_bytes,
       v.device_limit,
       v.billing_period
FROM subscriptions s
LEFT JOIN LATERAL (
  SELECT * FROM entitlements ent
  WHERE ent.subscription_id = s.id AND ent.status <> 'superseded'
  ORDER BY ent.ends_at DESC LIMIT 1
) e ON true
LEFT JOIN plan_versions v ON v.id = e.plan_version_id
LEFT JOIN plans p ON p.id = v.plan_id
LEFT JOIN plan_localizations l ON l.plan_id = p.id AND l.locale = sqlc.arg(locale)
WHERE s.user_id = sqlc.arg(user_id) AND s.id = sqlc.arg(subscription_id) AND s.status = 'active';

-- name: UpdateAccountProfile :one
-- The customer's own locale and timezone. Both are validated in Go against the
-- supported set before they reach here.
UPDATE users
SET locale = sqlc.arg(locale), timezone = sqlc.arg(timezone), updated_at = now()
WHERE id = sqlc.arg(user_id) AND status = 'active'
RETURNING *;

-- name: RenameAccountSubscription :one
-- The label is what every screen and notification uses to name a subscription,
-- which is what makes several concurrent ones legible.
UPDATE subscriptions
SET label = sqlc.arg(label), updated_at = now()
WHERE id = sqlc.arg(subscription_id) AND user_id = sqlc.arg(user_id) AND status = 'active'
RETURNING *;
