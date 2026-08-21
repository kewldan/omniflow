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
       v.billing_period,
       -- The unpaid order that opened this subscription, when one is still
       -- waiting. A subscription row is created with its order and survives the
       -- order going unpaid, so without this the dashboard shows "not active"
       -- with no way to the payment that would make it so.
       COALESCE(po.id::text, '')::text AS pending_order_id
FROM subscriptions s
LEFT JOIN LATERAL (
  SELECT * FROM entitlements ent
  WHERE ent.subscription_id = s.id AND ent.status <> 'superseded'
  -- The entitlement in force today wins over one scheduled to start later,
  -- so a downgrade deferred to the end of the period does not hide the plan
  -- the customer is actually using until it takes over.
  ORDER BY (ent.starts_at <= now()) DESC, ent.ends_at DESC LIMIT 1
) e ON true
LEFT JOIN LATERAL (
  SELECT o.id FROM orders o
  WHERE o.subscription_id = s.id AND o.state = 'pending'
  ORDER BY o.created_at DESC LIMIT 1
) po ON true
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
       v.billing_period,
       -- The unpaid order that opened this subscription, when one is still
       -- waiting. A subscription row is created with its order and survives the
       -- order going unpaid, so without this the dashboard shows "not active"
       -- with no way to the payment that would make it so.
       COALESCE(po.id::text, '')::text AS pending_order_id
FROM subscriptions s
LEFT JOIN LATERAL (
  SELECT * FROM entitlements ent
  WHERE ent.subscription_id = s.id AND ent.status <> 'superseded'
  -- The entitlement in force today wins over one scheduled to start later,
  -- so a downgrade deferred to the end of the period does not hide the plan
  -- the customer is actually using until it takes over.
  ORDER BY (ent.starts_at <= now()) DESC, ent.ends_at DESC LIMIT 1
) e ON true
LEFT JOIN LATERAL (
  SELECT o.id FROM orders o
  WHERE o.subscription_id = s.id AND o.state = 'pending'
  ORDER BY o.created_at DESC LIMIT 1
) po ON true
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

-- name: SetBotPreferenceLocale :exec
-- The bot reads bot_preferences.locale before it reads users.locale, so a
-- language chosen on the profile screen is written here too; otherwise the
-- profile claimed to set the bot's language and did not. An upsert, because a
-- customer the web created before this row existed has none yet.
INSERT INTO bot_preferences (user_id, locale)
VALUES (sqlc.arg(user_id), sqlc.arg(locale))
ON CONFLICT (user_id) DO UPDATE
SET locale = EXCLUDED.locale, updated_at = now();

-- name: RenameAccountSubscription :one
-- The label is what every screen and notification uses to name a subscription,
-- which is what makes several concurrent ones legible.
UPDATE subscriptions
SET label = sqlc.arg(label), updated_at = now()
WHERE id = sqlc.arg(subscription_id) AND user_id = sqlc.arg(user_id) AND status = 'active'
RETURNING *;
