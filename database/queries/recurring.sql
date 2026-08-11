-- Saved payment methods, auto-renew, and the dunning schedule.
--
-- Nothing here holds an instrument. A saved method is a provider-issued token
-- and a masked label the provider supplied; a charge is made by handing that
-- token back to the adapter that issued it.

-- ---------------------------------------------------------------------------
-- Saved payment methods
-- ---------------------------------------------------------------------------

-- name: ListPaymentMethods :many
SELECT * FROM payment_methods
WHERE user_id = $1 AND status <> 'revoked'
ORDER BY is_default DESC, created_at DESC;

-- name: GetPaymentMethod :one
SELECT * FROM payment_methods WHERE id = $1;

-- name: SavePaymentMethod :one
-- Re-presenting the same provider token is not a new method: the provider still
-- holds one binding, so the row is refreshed rather than duplicated. Consent is
-- re-stamped because the customer just granted it again.
INSERT INTO payment_methods (
  user_id, provider, merchant_id, provider_token, display_label, consent_at
) VALUES (
  sqlc.arg(user_id), sqlc.arg(provider), sqlc.arg(merchant_id),
  sqlc.arg(provider_token), sqlc.arg(display_label), now()
)
ON CONFLICT (provider, merchant_id, provider_token) DO UPDATE
SET display_label = EXCLUDED.display_label,
    status = 'active',
    revoked_at = NULL,
    consent_at = now(),
    updated_at = now()
RETURNING *;

-- name: ClearDefaultPaymentMethod :exec
UPDATE payment_methods
SET is_default = false, updated_at = now()
WHERE user_id = sqlc.arg(user_id) AND is_default;

-- name: SetDefaultPaymentMethod :one
UPDATE payment_methods
SET is_default = true, updated_at = now()
WHERE id = sqlc.arg(payment_method_id)
  AND user_id = sqlc.arg(user_id)
  AND status = 'active'
RETURNING *;

-- name: RevokePaymentMethod :one
-- Revocation is what a customer removing a card does. The row is kept so a
-- historical charge still resolves to the method that made it.
UPDATE payment_methods
SET status = 'revoked', is_default = false, revoked_at = now(), updated_at = now()
WHERE id = sqlc.arg(payment_method_id)
  AND user_id = sqlc.arg(user_id)
  AND status <> 'revoked'
RETURNING *;

-- name: MarkPaymentMethodStatus :one
UPDATE payment_methods
SET status = sqlc.arg(status),
    is_default = CASE WHEN sqlc.arg(status)::text = 'active' THEN is_default ELSE false END,
    last_used_at = CASE WHEN sqlc.arg(status)::text = 'active' THEN now() ELSE last_used_at END,
    updated_at = now()
WHERE id = sqlc.arg(payment_method_id) AND status <> 'revoked'
RETURNING *;

-- ---------------------------------------------------------------------------
-- Auto-renew
-- ---------------------------------------------------------------------------

-- name: GetAutoRenewSettings :one
SELECT * FROM auto_renew_settings
WHERE user_id = sqlc.arg(user_id)
  AND subscription_id IS NOT DISTINCT FROM sqlc.narg(subscription_id)::uuid;

-- name: UpsertAutoRenewSettings :one
-- `consent_at` is only ever written when auto-renew is being turned on, and it
-- is never cleared: a customer who disables and re-enables re-consents, and a
-- customer who disables keeps the record that they once agreed.
INSERT INTO auto_renew_settings (
  user_id, subscription_id, enabled, plan_version_id, provider, currency,
  funding, payment_method_id, lead_time_seconds, consent_at, state
) VALUES (
  sqlc.arg(user_id), sqlc.narg(subscription_id), sqlc.arg(enabled),
  sqlc.narg(plan_version_id), sqlc.narg(provider), sqlc.narg(currency),
  sqlc.arg(funding), sqlc.narg(payment_method_id), sqlc.arg(lead_time_seconds),
  CASE WHEN sqlc.arg(enabled)::boolean THEN now() ELSE NULL END,
  CASE WHEN sqlc.arg(enabled)::boolean THEN 'scheduled' ELSE 'idle' END
)
ON CONFLICT (user_id, subscription_id) DO UPDATE
SET enabled = EXCLUDED.enabled,
    plan_version_id = EXCLUDED.plan_version_id,
    provider = EXCLUDED.provider,
    currency = EXCLUDED.currency,
    funding = EXCLUDED.funding,
    payment_method_id = EXCLUDED.payment_method_id,
    lead_time_seconds = EXCLUDED.lead_time_seconds,
    consent_at = CASE
      WHEN EXCLUDED.enabled THEN now()
      ELSE auto_renew_settings.consent_at
    END,
    cancelled_at = CASE WHEN EXCLUDED.enabled THEN NULL ELSE now() END,
    state = CASE WHEN EXCLUDED.enabled THEN 'scheduled' ELSE 'idle' END,
    updated_at = now()
RETURNING *;

-- name: SetAutoRenewState :one
UPDATE auto_renew_settings
SET state = sqlc.arg(state),
    last_attempt_at = sqlc.narg(last_attempt_at),
    last_failure_code = sqlc.narg(last_failure_code),
    updated_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND subscription_id IS NOT DISTINCT FROM sqlc.narg(subscription_id)::uuid
RETURNING *;

-- name: ListAutoRenewDue :many
-- Subscriptions whose entitlement ends inside the configured lead time and that
-- have no renewal attempt outstanding for the cycle. The cycle key is derived
-- here, from the entitlement identity and its end instant, so the worker and
-- the dunning table agree on what "this renewal" means without a second round
-- trip.
SELECT
  a.*,
  e.id AS entitlement_id,
  e.ends_at AS entitlement_ends_at,
  ((e.id::text || ':' || extract(epoch FROM e.ends_at)::bigint::text))::text AS cycle_key
FROM auto_renew_settings a
JOIN entitlements e ON e.subscription_id IS NOT DISTINCT FROM a.subscription_id
  AND e.user_id = a.user_id
  AND e.status IN ('active', 'limited')
JOIN users u ON u.id = a.user_id AND u.status = 'active'
WHERE a.enabled
  AND a.state IN ('scheduled', 'dunning')
  AND e.ends_at <= now() + make_interval(secs => a.lead_time_seconds)
  AND NOT EXISTS (
    SELECT 1 FROM dunning_attempts d
    WHERE d.cycle_key = (e.id::text || ':' || extract(epoch FROM e.ends_at)::bigint::text)
      AND d.outcome IN ('scheduled', 'succeeded')
  )
ORDER BY e.ends_at
LIMIT sqlc.arg(page_size);

-- ---------------------------------------------------------------------------
-- Dunning
-- ---------------------------------------------------------------------------

-- name: ScheduleDunningAttempt :one
-- The (cycle_key, attempt) uniqueness is the idempotency guard: a replayed job
-- inserts nothing and the caller reads back the attempt that already exists.
INSERT INTO dunning_attempts (
  user_id, subscription_id, cycle_key, attempt, funding, payment_method_id, scheduled_for
) VALUES (
  sqlc.arg(user_id), sqlc.narg(subscription_id), sqlc.arg(cycle_key), sqlc.arg(attempt),
  sqlc.arg(funding), sqlc.narg(payment_method_id), sqlc.arg(scheduled_for)
)
ON CONFLICT (cycle_key, attempt) DO NOTHING
RETURNING *;

-- name: GetDunningAttempt :one
SELECT * FROM dunning_attempts
WHERE cycle_key = sqlc.arg(cycle_key) AND attempt = sqlc.arg(attempt);

-- name: ListDueDunningAttempts :many
SELECT * FROM dunning_attempts
WHERE outcome = 'scheduled' AND scheduled_for <= now()
ORDER BY scheduled_for
LIMIT sqlc.arg(page_size);

-- name: ResolveDunningAttempt :one
-- Only a scheduled attempt resolves, so a retried worker cannot rewrite an
-- outcome that has already been recorded.
UPDATE dunning_attempts
SET outcome = sqlc.arg(outcome),
    failure_code = sqlc.narg(failure_code),
    order_id = sqlc.narg(order_id),
    occurred_at = now()
WHERE id = sqlc.arg(attempt_id) AND outcome = 'scheduled'
RETURNING *;

-- name: MarkDunningNotified :one
UPDATE dunning_attempts
SET notified_at = now()
WHERE id = sqlc.arg(attempt_id) AND notified_at IS NULL
RETURNING *;

-- name: CountDunningAttemptsForCycle :one
SELECT count(*)::bigint FROM dunning_attempts WHERE cycle_key = $1;

-- name: ListDunningAttemptsForCustomer :many
SELECT * FROM dunning_attempts
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT sqlc.arg(page_size);

-- name: ListRecentDunningFailures :many
-- The panel's review queue: failed and abandoned charges, newest first, with
-- the customer attached so the list is usable without a second lookup per row.
SELECT sqlc.embed(d), u.status AS customer_status
FROM dunning_attempts d
JOIN users u ON u.id = d.user_id
WHERE d.outcome IN ('failed', 'abandoned')
  AND (
    sqlc.narg(cursor_created_at)::timestamptz IS NULL
    OR (d.created_at, d.id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
ORDER BY d.created_at DESC, d.id DESC
LIMIT sqlc.arg(page_size);
