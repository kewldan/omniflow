-- Queries behind the Remnawave synchronisation surfaces: naming new panel
-- users, webhook intake, device aliases, and the lifecycle passes.

-- name: GetCustomerTelegramID :one
SELECT (provider_subject)::bigint AS telegram_id
FROM identities
WHERE user_id = sqlc.arg(user_id) AND provider = 'telegram' AND status = 'active'
ORDER BY created_at
LIMIT 1;

-- name: InsertRemnawaveWebhookEvent :one
-- A retried delivery carries the same bytes and is a no-op: no row comes back.
INSERT INTO remnawave_webhook_events (event, body_sha256, remnawave_user_id, details, status, processed_at)
VALUES (
  sqlc.arg(event), sqlc.arg(body_sha256), sqlc.narg(remnawave_user_id), sqlc.arg(details),
  sqlc.arg(status),
  CASE WHEN sqlc.arg(status)::text = 'pending' THEN NULL ELSE now() END
)
ON CONFLICT (body_sha256) DO NOTHING
RETURNING id;

-- name: ListPendingRemnawaveCustomerEvents :many
-- Each pending event with the subscription it concerns. The subscription is
-- matched on its own Remnawave user first and on the customer-level mapping
-- second, which is how a customer imported before subscriptions existed is
-- still found.
SELECT e.id, e.event, e.details, e.received_at,
       COALESCE(s.user_id, r.user_id) AS user_id,
       s.id AS subscription_id,
       COALESCE(s.label, '')::text AS subscription_label
FROM remnawave_webhook_events e
LEFT JOIN subscriptions s ON s.remnawave_user_id = e.remnawave_user_id AND s.status = 'active'
LEFT JOIN remnawave_users r ON s.id IS NULL AND r.remnawave_id = e.remnawave_user_id
WHERE e.status = 'pending'
ORDER BY e.id
LIMIT sqlc.arg(batch_size);

-- name: CompleteRemnawaveWebhookEvent :exec
UPDATE remnawave_webhook_events
SET status = sqlc.arg(status), processed_at = now()
WHERE id = sqlc.arg(id) AND status = 'pending';

-- name: CustomerRotatedLinkRecently :one
-- A customer who re-issued their own link has just seen the result; the
-- panel's echo of it is not news to them.
SELECT EXISTS (
  SELECT 1 FROM customer_security_events
  WHERE user_id = sqlc.arg(user_id) AND event = 'subscription_key_rotated'
    AND occurred_at > now() - interval '10 minutes'
) AS rotated;

-- name: CountActiveSubscriptionsForCustomer :one
SELECT count(*)::int AS subscriptions FROM subscriptions WHERE user_id = sqlc.arg(user_id) AND status = 'active';

-- name: GetNotificationDeliveryStatus :one
SELECT status FROM notification_deliveries
WHERE user_id = sqlc.arg(user_id) AND kind = sqlc.arg(kind)
  AND subscription_id IS NOT DISTINCT FROM sqlc.narg(subscription_id)
  AND dedupe_key = sqlc.arg(dedupe_key);

-- name: ClaimSubscriptionLinkRotation :one
-- Takes the re-issue slot of one subscription when the cooldown has passed,
-- returning when it was last taken so a failed re-issue can give it back. No
-- row means the subscription is not the customer's or is cooling down.
WITH previous AS (
  SELECT x.id AS subscription_id, x.link_rotated_at FROM subscriptions x
  WHERE x.id = sqlc.arg(subscription_id) AND x.user_id = sqlc.arg(user_id) AND x.status = 'active'
  FOR UPDATE
)
UPDATE subscriptions s SET link_rotated_at = now()
FROM previous
WHERE s.id = previous.subscription_id
  AND (previous.link_rotated_at IS NULL
       OR previous.link_rotated_at <= now() - make_interval(secs => sqlc.arg(cooldown_seconds)::float8))
RETURNING previous.link_rotated_at AS previous_rotated_at;

-- name: RestoreSubscriptionLinkRotation :exec
UPDATE subscriptions SET link_rotated_at = sqlc.narg(rotated_at)
WHERE id = sqlc.arg(subscription_id) AND user_id = sqlc.arg(user_id);

-- name: GetSubscriptionLinkRotatedAt :one
SELECT link_rotated_at FROM subscriptions
WHERE id = sqlc.arg(subscription_id) AND user_id = sqlc.arg(user_id);

-- name: ListDeviceAliases :many
SELECT device_handle, alias FROM device_aliases
WHERE subscription_id = sqlc.arg(subscription_id) AND user_id = sqlc.arg(user_id);

-- name: UpsertDeviceAlias :exec
INSERT INTO device_aliases (subscription_id, user_id, device_handle, alias)
VALUES (sqlc.arg(subscription_id), sqlc.arg(user_id), sqlc.arg(device_handle), sqlc.arg(alias))
ON CONFLICT (subscription_id, device_handle) DO UPDATE SET alias = EXCLUDED.alias, updated_at = now();

-- name: DeleteDeviceAlias :exec
DELETE FROM device_aliases
WHERE subscription_id = sqlc.arg(subscription_id) AND user_id = sqlc.arg(user_id) AND device_handle = sqlc.arg(device_handle);

-- name: GetDeviceAlias :one
SELECT alias FROM device_aliases
WHERE subscription_id = sqlc.arg(subscription_id) AND device_handle = sqlc.arg(device_handle);
