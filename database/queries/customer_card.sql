-- Operator controls on the customer card: restrictions, trial reset,
-- auto-renew, activity, and the advanced list.

-- name: ListCustomerRestrictions :many
SELECT * FROM customer_restrictions WHERE user_id = sqlc.arg(user_id) ORDER BY kind;

-- name: CustomerRestricted :one
SELECT EXISTS (
  SELECT 1 FROM customer_restrictions WHERE user_id = sqlc.arg(user_id) AND kind = sqlc.arg(kind)
)::boolean AS restricted;

-- name: PutCustomerRestriction :one
INSERT INTO customer_restrictions (user_id, kind, reason, created_by)
VALUES (sqlc.arg(user_id), sqlc.arg(kind), sqlc.arg(reason), sqlc.narg(created_by))
ON CONFLICT (user_id, kind) DO UPDATE SET reason = EXCLUDED.reason, created_by = EXCLUDED.created_by, created_at = now()
RETURNING *;

-- name: DeleteCustomerRestriction :execrows
DELETE FROM customer_restrictions WHERE user_id = sqlc.arg(user_id) AND kind = sqlc.arg(kind);

-- name: ResetCustomerTrial :execrows
-- Forgetting the trial claim lets the customer take a trial again. The order
-- and entitlement the trial produced stay: they are history.
DELETE FROM trial_claims WHERE user_id = sqlc.arg(user_id);

-- name: SetCustomerAutoRenew :execrows
-- Turning auto-renew off is always allowed. Turning it on is allowed only
-- where the customer consented before and the renewal is fully described:
-- an operator cannot consent on a customer's behalf.
UPDATE auto_renew_settings
SET enabled = sqlc.arg(enabled),
    cancelled_at = CASE WHEN sqlc.arg(enabled)::boolean THEN NULL ELSE now() END,
    updated_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND (NOT sqlc.arg(enabled)::boolean OR (consent_at IS NOT NULL AND plan_version_id IS NOT NULL
       AND provider IS NOT NULL AND currency IS NOT NULL));

-- name: GetCustomerAutoRenew :one
SELECT enabled, (consent_at IS NOT NULL)::boolean AS consented, provider, funding
FROM auto_renew_settings WHERE user_id = sqlc.arg(user_id);

-- name: TouchCustomerActivity :exec
-- Written at most once every five minutes per customer.
UPDATE users SET last_activity_at = now()
WHERE id = sqlc.arg(user_id)
  AND (last_activity_at IS NULL OR last_activity_at < now() - interval '5 minutes');

-- name: RecordCustomerActivity :exec
INSERT INTO customer_activity (user_id, surface, action) VALUES (sqlc.arg(user_id), sqlc.arg(surface), sqlc.arg(action));

-- name: ListCustomerActivity :many
SELECT surface, action, occurred_at FROM customer_activity
WHERE user_id = sqlc.arg(user_id)
ORDER BY occurred_at DESC
LIMIT sqlc.arg(page_size);

-- name: CustomerLastActivity :one
SELECT last_activity_at FROM users WHERE id = sqlc.arg(user_id);

-- name: PruneCustomerActivity :execrows
DELETE FROM customer_activity WHERE occurred_at < now() - interval '90 days';

-- name: ButtonClickStats :many
-- How often each bot screen or button was used, with no customer in it.
SELECT action, count(*)::bigint AS clicks, count(DISTINCT user_id)::bigint AS customers
FROM customer_activity
WHERE surface = 'bot' AND occurred_at >= sqlc.arg(since)
GROUP BY action
ORDER BY clicks DESC
LIMIT 200;

-- name: SearchCustomersAdvanced :many
-- The customer list's sorted and filtered views. Sorted views are not
-- paginated: they answer "who are the first 200 by this measure".
SELECT sqlc.embed(u), r.telegram_id,
  COALESCE((SELECT sum(l.amount_minor) FROM ledger_entries l
            WHERE l.user_id = u.id AND l.account_type = 'customer_wallet'), 0)::bigint AS balance_minor,
  (SELECT max(e.ends_at) FROM entitlements e
   WHERE e.user_id = u.id AND e.status IN ('active', 'limited', 'paused'))::timestamptz AS ends_at
FROM users u
LEFT JOIN remnawave_users r ON r.user_id = u.id
WHERE u.status <> 'merged'
  AND (sqlc.narg(status)::text IS NULL OR u.status = sqlc.narg(status))
  AND (sqlc.narg(expiring_days)::int IS NULL OR EXISTS (
        SELECT 1 FROM entitlements e WHERE e.user_id = u.id AND e.status IN ('active', 'limited')
          AND e.ends_at > now() AND e.ends_at <= now() + make_interval(days => sqlc.narg(expiring_days)::int)))
  AND (NOT sqlc.arg(with_balance)::boolean OR EXISTS (
        SELECT 1 FROM ledger_entries l WHERE l.user_id = u.id AND l.account_type = 'customer_wallet'
        GROUP BY l.currency HAVING sum(l.amount_minor) > 0))
  AND (NOT sqlc.arg(auto_renew)::boolean OR EXISTS (
        SELECT 1 FROM auto_renew_settings a WHERE a.user_id = u.id AND a.enabled))
  AND (NOT sqlc.arg(restricted)::boolean OR EXISTS (
        SELECT 1 FROM customer_restrictions c WHERE c.user_id = u.id))
  AND (NOT sqlc.arg(trial_only)::boolean OR (EXISTS (SELECT 1 FROM trial_claims t
          WHERE t.user_id = u.id AND t.origin <> 'bedolaga_history')
        AND NOT EXISTS (SELECT 1 FROM orders o WHERE o.user_id = u.id AND o.paid_minor > 0)
        AND NOT EXISTS (SELECT 1 FROM legacy_payments lp WHERE lp.user_id = u.id)))
  AND (sqlc.narg(campaign)::text IS NULL OR EXISTS (
        SELECT 1 FROM customer_campaign_attributions c JOIN ad_campaigns ac ON ac.id = c.campaign_id
        WHERE c.user_id = u.id AND ac.code = sqlc.narg(campaign)::text))
  AND (sqlc.narg(active_within_days)::int IS NULL
        OR u.last_activity_at >= now() - make_interval(days => sqlc.narg(active_within_days)::int))
  AND (sqlc.narg(traffic_percent)::int IS NULL OR EXISTS (
        SELECT 1 FROM entitlements e
        JOIN remnawave_users ru ON ru.user_id = e.user_id
        WHERE e.user_id = u.id AND e.status IN ('active', 'limited') AND e.traffic_allowance_bytes > 0
          AND COALESCE((ru.observed_state ->> 'usedTrafficBytes')::bigint, 0) * 100
              >= e.traffic_allowance_bytes * sqlc.narg(traffic_percent)::int))
ORDER BY
  CASE WHEN sqlc.arg(sort)::text = 'balance' THEN COALESCE((SELECT sum(l.amount_minor) FROM ledger_entries l
            WHERE l.user_id = u.id AND l.account_type = 'customer_wallet'), 0) END DESC NULLS LAST,
  CASE WHEN sqlc.arg(sort)::text = 'expiry' THEN (SELECT max(e.ends_at) FROM entitlements e
            WHERE e.user_id = u.id AND e.status IN ('active', 'limited', 'paused')) END ASC NULLS LAST,
  CASE WHEN sqlc.arg(sort)::text = 'activity' THEN u.last_activity_at END DESC NULLS LAST,
  CASE WHEN sqlc.arg(sort)::text = 'oldest' THEN u.created_at END ASC,
  u.created_at DESC
LIMIT 200;

-- name: LatestLivePlanVersion :one
-- The plan version an operator means when they pick a plan by name.
SELECT v.id, pr.currency FROM plan_versions v
JOIN plan_prices pr ON pr.plan_version_id = v.id
WHERE v.plan_id = sqlc.arg(plan_id) AND v.retired_at IS NULL
ORDER BY v.version DESC, pr.currency
LIMIT 1;

-- name: ListCustomerLegacyRecords :many
-- The previous bot's rows about one customer, newest first.
SELECT kind, source_id, occurred_at, data
FROM legacy_records
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.narg(kind)::text IS NULL OR kind = sqlc.narg(kind)::text)
ORDER BY occurred_at DESC NULLS LAST, kind, source_id
LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(skip);

-- name: CountCustomerLegacyRecords :many
SELECT kind, count(*)::bigint AS count
FROM legacy_records
WHERE user_id = sqlc.arg(user_id)
GROUP BY kind
ORDER BY kind;
