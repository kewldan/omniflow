-- Omniflow v0.5 queries: subscriptions, wallet top-up, carts, add-ons,
-- operator notifications, backups, maintenance mode, and retention.

-- ---------------------------------------------------------------------------
-- Subscriptions
-- ---------------------------------------------------------------------------

-- name: ListSubscriptions :many
SELECT * FROM subscriptions
WHERE user_id = $1 AND status = 'active'
ORDER BY slot;

-- name: GetSubscription :one
SELECT * FROM subscriptions WHERE id = $1;

-- name: GetCustomerSubscription :one
SELECT * FROM subscriptions WHERE id = sqlc.arg(subscription_id) AND user_id = sqlc.arg(user_id);

-- name: GetSubscriptionByRemnawaveUser :one
SELECT * FROM subscriptions WHERE remnawave_user_id = $1;

-- name: GetPrimarySubscription :one
SELECT * FROM subscriptions
WHERE user_id = $1 AND status = 'active'
ORDER BY slot
LIMIT 1;

-- name: LockCustomerSubscriptions :exec
-- Serialises concurrent purchases for one customer so two taps cannot both
-- observe a stale count and both pass a concurrency limit check. It is a
-- statement of its own rather than a CTE, because a planner is free not to
-- evaluate a CTE whose rows are never needed.
SELECT pg_advisory_xact_lock(hashtextextended('omniflow:subscriptions:' || $1::text, 0));

-- name: CountActiveSubscriptions :one
-- Callers must hold LockCustomerSubscriptions for this count to be meaningful.
SELECT
  count(*)::integer AS total_count,
  count(*) FILTER (
    WHERE EXISTS (
      SELECT 1 FROM entitlements e
      JOIN plan_versions pv ON pv.id = e.plan_version_id
      WHERE e.subscription_id = s.id AND pv.plan_id = sqlc.arg(plan_id)
        AND e.status IN ('pending', 'active', 'limited', 'disabled')
    )
  )::integer AS plan_count
FROM subscriptions s
WHERE s.user_id = sqlc.arg(user_id) AND s.status = 'active';

-- name: CreateSubscription :one
INSERT INTO subscriptions (user_id, slot, label)
SELECT sqlc.arg(user_id), COALESCE(max(slot), 0) + 1, sqlc.arg(label)
FROM subscriptions WHERE user_id = sqlc.arg(user_id)
RETURNING *;

-- name: RenameSubscription :one
UPDATE subscriptions SET label = sqlc.arg(label), updated_at = now()
WHERE id = sqlc.arg(subscription_id) AND user_id = sqlc.arg(user_id)
RETURNING *;

-- name: DeleteGhostSubscriptions :many
-- Removes a customer's subscriptions that exist only because an order once
-- opened them and that order then closed unpaid.
--
-- A subscription row is created when an order for a new subscription is
-- created, before any money moves, so a cancelled or expired order leaves a
-- subscription that never had an entitlement and never will. Left alone, each
-- one takes a slot toward the concurrency limit and a place in every picker.
--
-- The predicate is deliberately strict: nothing may reference the row except
-- orders that closed unpaid. An entitlement, a live order, a Remnawave user,
-- an auto-renew setting, a notification, a cart, a checkout session, or a
-- dunning attempt each keeps it — those are the records a customer or an
-- operator can still reach it through. The closed orders are detached first,
-- because the foreign key would otherwise refuse the delete, and a closed
-- order targets nothing any more.
--
-- Callers hold LockCustomerSubscriptions so a concurrent purchase cannot
-- resolve a subscription this statement is about to remove.
WITH candidates AS (
  SELECT s.id
  FROM subscriptions s
  WHERE s.user_id = sqlc.arg(user_id)
    AND s.remnawave_user_id IS NULL
    AND NOT EXISTS (SELECT 1 FROM entitlements e WHERE e.subscription_id = s.id)
    AND NOT EXISTS (
      SELECT 1 FROM orders o
      WHERE o.subscription_id = s.id AND o.state NOT IN ('cancelled', 'expired')
    )
    AND NOT EXISTS (SELECT 1 FROM auto_renew_settings a WHERE a.subscription_id = s.id)
    AND NOT EXISTS (SELECT 1 FROM notification_deliveries n WHERE n.subscription_id = s.id)
    AND NOT EXISTS (SELECT 1 FROM carts c WHERE c.subscription_id = s.id)
    AND NOT EXISTS (SELECT 1 FROM bot_checkout_sessions b WHERE b.subscription_id = s.id)
    AND NOT EXISTS (SELECT 1 FROM dunning_attempts d WHERE d.subscription_id = s.id)
  FOR UPDATE OF s
), detached AS (
  UPDATE orders SET subscription_id = NULL, updated_at = now()
  WHERE subscription_id IN (SELECT id FROM candidates)
)
DELETE FROM subscriptions WHERE id IN (SELECT id FROM candidates)
RETURNING *;

-- name: CloseSubscription :one
UPDATE subscriptions
SET status = 'closed', closed_at = now(), updated_at = now()
WHERE id = sqlc.arg(subscription_id) AND user_id = sqlc.arg(user_id) AND status = 'active'
RETURNING *;

-- name: UpsertSubscriptionRemnawaveUser :one
UPDATE subscriptions
SET remnawave_user_id = sqlc.arg(remnawave_user_id),
    remnawave_username = COALESCE(sqlc.narg(remnawave_username), remnawave_username),
    observed_state = sqlc.arg(observed_state), reconciled_at = now(), updated_at = now()
WHERE id = sqlc.arg(subscription_id)
RETURNING *;

-- name: ListSubscriptionsForAlerts :many
SELECT s.*, u.locale
FROM subscriptions s
JOIN users u ON u.id = s.user_id
WHERE s.status = 'active' AND s.remnawave_user_id IS NOT NULL
ORDER BY s.user_id, s.slot
LIMIT $1;

-- ---------------------------------------------------------------------------
-- Wallet top-up
-- ---------------------------------------------------------------------------

-- name: CreateWalletTopup :one
INSERT INTO wallet_topups (order_id, user_id, currency, requested_minor)
VALUES ($1, $2, $3, $4)
ON CONFLICT (order_id) DO UPDATE SET order_id = EXCLUDED.order_id
RETURNING *;

-- name: LockWalletTopup :one
SELECT * FROM wallet_topups WHERE order_id = $1 FOR UPDATE;

-- name: CreditWalletTopup :one
UPDATE wallet_topups
SET credited_minor = sqlc.arg(credited_minor),
    ledger_transaction_id = sqlc.arg(ledger_transaction_id), credited_at = now()
WHERE order_id = sqlc.arg(order_id)
RETURNING *;

-- name: SumRecentTopups :one
-- The rolling window counts what was actually credited, so a failed or expired
-- attempt never consumes a customer's allowance.
SELECT COALESCE(sum(credited_minor), 0)::bigint AS credited_minor
FROM wallet_topups
WHERE user_id = sqlc.arg(user_id) AND currency = sqlc.arg(currency)
  AND credited_at IS NOT NULL
  AND credited_at > now() - make_interval(secs => sqlc.arg(window_seconds)::double precision);

-- name: ListWalletTopups :many
SELECT t.*, o.state, o.expires_at, COALESCE(pi.provider, '') AS provider,
       COALESCE(pi.status, '') AS payment_status, COALESCE(pi.checkout_url, '') AS checkout_url
FROM wallet_topups t
JOIN orders o ON o.id = t.order_id
LEFT JOIN LATERAL (
  SELECT * FROM payment_intents intent WHERE intent.order_id = o.id
  ORDER BY intent.created_at DESC LIMIT 1
) pi ON true
WHERE t.user_id = sqlc.arg(user_id)
ORDER BY t.created_at DESC
LIMIT sqlc.arg(row_limit);

-- ---------------------------------------------------------------------------
-- Cart and deferred purchase
-- ---------------------------------------------------------------------------

-- name: UpsertCart :one
INSERT INTO carts (
  user_id, subscription_id, plan_version_id, operation, currency, promo_code,
  selected_squad_ids, auto_purchase, idempotency_key, expires_at, kind
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'plan')
ON CONFLICT (user_id) WHERE status = 'open'
DO UPDATE SET subscription_id = EXCLUDED.subscription_id,
              kind = EXCLUDED.kind,
              plan_version_id = EXCLUDED.plan_version_id,
              operation = EXCLUDED.operation,
              currency = EXCLUDED.currency,
              promo_code = EXCLUDED.promo_code,
              selected_squad_ids = EXCLUDED.selected_squad_ids,
              auto_purchase = EXCLUDED.auto_purchase,
              expires_at = EXCLUDED.expires_at,
              idempotency_key = EXCLUDED.idempotency_key,
              last_failure = NULL,
              attempt_count = 0,
              updated_at = now()
RETURNING *;

-- name: GetOpenCart :one
SELECT * FROM carts WHERE user_id = $1 AND status = 'open';

-- name: LockOpenCart :one
SELECT * FROM carts WHERE user_id = $1 AND status = 'open' FOR UPDATE;

-- name: SetCartAutoPurchase :one
UPDATE carts SET auto_purchase = sqlc.arg(auto_purchase), updated_at = now()
WHERE user_id = sqlc.arg(user_id) AND status = 'open'
RETURNING *;

-- name: MarkCartPurchased :one
UPDATE carts
SET status = 'purchased', order_id = sqlc.arg(order_id), last_failure = NULL, updated_at = now()
WHERE id = sqlc.arg(cart_id) AND status = 'open'
RETURNING *;

-- name: RecordCartFailure :one
UPDATE carts
SET last_failure = sqlc.arg(last_failure), attempt_count = attempt_count + 1, updated_at = now()
WHERE id = sqlc.arg(cart_id)
RETURNING *;

-- name: CancelCart :one
UPDATE carts SET status = 'cancelled', updated_at = now()
WHERE user_id = $1 AND status = 'open'
RETURNING *;

-- name: ExpireCarts :many
UPDATE carts SET status = 'expired', updated_at = now()
WHERE status = 'open' AND expires_at <= now()
RETURNING *;

-- name: ListAutoPurchaseCarts :many
-- Only carts whose customer holds wallet history are considered, so the sweep
-- never re-prices a cart that cannot possibly be covered yet.
SELECT c.id, c.user_id
FROM carts c
WHERE c.status = 'open' AND c.auto_purchase AND c.expires_at > now()
  AND EXISTS (
    SELECT 1 FROM ledger_entries e
    WHERE e.account_type = 'customer_wallet' AND e.user_id = c.user_id AND e.currency = c.currency
  )
ORDER BY c.updated_at
LIMIT $1;

-- name: SetCartAddon :exec
INSERT INTO cart_addons (cart_id, addon_version_id, quantity)
VALUES ($1, $2, $3)
ON CONFLICT (cart_id, addon_version_id) DO UPDATE SET quantity = EXCLUDED.quantity;

-- name: DeleteCartAddon :exec
DELETE FROM cart_addons WHERE cart_id = $1 AND addon_version_id = $2;

-- name: ListCartAddons :many
SELECT * FROM cart_addons WHERE cart_id = $1 ORDER BY addon_version_id;

-- ---------------------------------------------------------------------------
-- Add-ons
-- ---------------------------------------------------------------------------

-- name: ListPlanAddons :many
SELECT a.id, a.code, a.kind, a.sort_order, l.name, l.description,
       v.id AS addon_version_id, v.version, v.traffic_bytes, v.device_slots,
       v.remnawave_squad_ids, v.max_quantity, v.proration,
       pr.currency, pr.amount_minor
FROM plan_version_addons pa
JOIN addons a ON a.id = pa.addon_id
JOIN addon_localizations l ON l.addon_id = a.id AND l.locale = sqlc.arg(locale)
JOIN LATERAL (
  SELECT * FROM addon_versions av
  WHERE av.addon_id = a.id AND av.retired_at IS NULL
  ORDER BY av.version DESC LIMIT 1
) v ON true
JOIN addon_prices pr ON pr.addon_version_id = v.id AND pr.currency = sqlc.arg(currency)
WHERE pa.plan_version_id = sqlc.arg(plan_version_id)
  AND a.visible AND a.archived_at IS NULL
ORDER BY a.sort_order, a.code;

-- name: GetAddonVersionForOrder :one
SELECT a.id AS addon_id, a.code, a.kind, a.archived_at, v.*, pr.currency, pr.amount_minor
FROM addon_versions v
JOIN addons a ON a.id = v.addon_id
JOIN addon_prices pr ON pr.addon_version_id = v.id
WHERE v.id = sqlc.arg(addon_version_id) AND pr.currency = sqlc.arg(currency);

-- name: IsAddonOfferedForPlan :one
SELECT EXISTS (
  SELECT 1 FROM plan_version_addons pa
  JOIN addon_versions av ON av.addon_id = pa.addon_id
  WHERE pa.plan_version_id = sqlc.arg(plan_version_id) AND av.id = sqlc.arg(addon_version_id)
) AS offered;

-- name: InsertOrderAddonLine :one
INSERT INTO order_addon_lines (
  order_id, addon_id, addon_version_id, quantity, unit_amount_minor,
  charged_minor, proration, snapshot
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (order_id, addon_version_id) DO UPDATE SET snapshot = order_addon_lines.snapshot
RETURNING *;

-- name: ListOrderAddonLines :many
SELECT * FROM order_addon_lines WHERE order_id = $1 ORDER BY addon_version_id;

-- name: InsertEntitlementAddon :one
INSERT INTO entitlement_addons (
  entitlement_id, order_id, addon_version_id, quantity, traffic_bytes,
  device_slots, remnawave_squad_ids
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (order_id, addon_version_id) DO NOTHING
RETURNING *;

-- name: ApplyEntitlementAddonTotals :one
-- Add-on capacity is folded into the entitlement itself, so the existing
-- fulfillment pipeline reads one desired state and never sums add-ons again.
UPDATE entitlements
SET traffic_allowance_bytes = CASE
      WHEN traffic_allowance_bytes IS NULL THEN NULL
      ELSE traffic_allowance_bytes + sqlc.arg(extra_traffic_bytes)::bigint
    END,
    device_limit = CASE
      WHEN device_limit IS NULL THEN NULL
      ELSE device_limit + sqlc.arg(extra_device_slots)::integer
    END,
    remnawave_squad_ids = (
      SELECT COALESCE(array_agg(DISTINCT squad), '{}')
      FROM unnest(remnawave_squad_ids || sqlc.arg(extra_squad_ids)::uuid[]) AS squad
    ),
    updated_at = now()
WHERE id = sqlc.arg(entitlement_id)
RETURNING *;

-- name: ListPlanVersionSquads :many
SELECT * FROM plan_version_squads WHERE plan_version_id = $1 ORDER BY sort_order, squad_id;

-- name: CountPlanVersionSquads :one
SELECT count(*)::integer AS selectable_count
FROM plan_version_squads
WHERE plan_version_id = sqlc.arg(plan_version_id)
  AND squad_id = ANY(sqlc.arg(squad_ids)::uuid[]);

-- ---------------------------------------------------------------------------
-- Operator notifications
-- ---------------------------------------------------------------------------

-- name: UpsertOperatorTopic :one
INSERT INTO operator_topics (kind, chat_id)
VALUES ($1, $2)
ON CONFLICT (kind) DO UPDATE
SET chat_id = EXCLUDED.chat_id,
    topic_id = CASE WHEN operator_topics.chat_id = EXCLUDED.chat_id THEN operator_topics.topic_id ELSE NULL END,
    status = CASE WHEN operator_topics.chat_id = EXCLUDED.chat_id THEN operator_topics.status ELSE 'pending' END,
    updated_at = now()
RETURNING *;

-- name: GetOperatorTopic :one
SELECT * FROM operator_topics WHERE kind = $1;

-- name: ListOperatorTopics :many
SELECT * FROM operator_topics ORDER BY kind;

-- name: BindOperatorTopic :one
UPDATE operator_topics
SET topic_id = sqlc.arg(topic_id), status = 'bound', last_error_code = NULL, updated_at = now()
WHERE kind = sqlc.arg(kind)
RETURNING *;

-- name: FailOperatorTopic :one
UPDATE operator_topics
SET topic_id = NULL, status = 'failed', last_error_code = sqlc.arg(last_error_code), updated_at = now()
WHERE kind = sqlc.arg(kind)
RETURNING *;

-- name: EnqueueOperatorNotification :one
INSERT INTO operator_notifications (kind, dedupe_key, payload)
VALUES ($1, $2, $3)
ON CONFLICT (kind, dedupe_key) DO NOTHING
RETURNING *;

-- name: ListPendingOperatorNotifications :many
SELECT * FROM operator_notifications
WHERE status = 'pending'
ORDER BY created_at
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: CompleteOperatorNotification :one
UPDATE operator_notifications
SET status = sqlc.arg(status), error_code = sqlc.narg(error_code),
    sent_at = CASE WHEN sqlc.arg(status) = 'sent' THEN now() ELSE NULL END
WHERE id = sqlc.arg(notification_id)
RETURNING *;

-- name: CountRecentOperatorNotifications :one
SELECT count(*)::integer AS sent_count
FROM operator_notifications
WHERE kind = sqlc.arg(kind) AND status = 'sent'
  AND sent_at > now() - make_interval(secs => sqlc.arg(window_seconds)::double precision);

-- ---------------------------------------------------------------------------
-- Backups
-- ---------------------------------------------------------------------------

-- name: CreateBackup :one
INSERT INTO backups (kind, file_name, encrypted, requested_by, retain_until)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: CompleteBackup :one
UPDATE backups
SET status = 'completed', size_bytes = sqlc.arg(size_bytes), sha256 = sqlc.arg(sha256),
    verified_at = now(), completed_at = now()
WHERE id = sqlc.arg(backup_id)
RETURNING *;

-- name: FailBackup :one
UPDATE backups
SET status = 'failed', error_code = sqlc.arg(error_code), completed_at = now()
WHERE id = sqlc.arg(backup_id)
RETURNING *;

-- name: GetBackup :one
SELECT * FROM backups WHERE id = $1;

-- name: GetLatestBackup :one
SELECT * FROM backups WHERE status = 'completed' ORDER BY completed_at DESC LIMIT 1;

-- name: ListBackups :many
SELECT * FROM backups ORDER BY started_at DESC LIMIT $1;

-- name: ListExpiredBackups :many
SELECT * FROM backups WHERE status = 'completed' AND retain_until <= now() ORDER BY retain_until LIMIT $1;

-- name: MarkBackupPruned :one
UPDATE backups SET status = 'pruned' WHERE id = $1 RETURNING *;

-- name: CountRunningBackups :one
SELECT count(*)::integer AS running_count FROM backups WHERE status = 'running';

-- name: CreateBackupRestore :one
INSERT INTO backup_restores (backup_id, operator_id, reason)
VALUES ($1, $2, $3)
RETURNING *;

-- name: CompleteBackupRestore :one
UPDATE backup_restores
SET status = sqlc.arg(status), error_code = sqlc.narg(error_code), completed_at = now()
WHERE id = sqlc.arg(restore_id)
RETURNING *;

-- ---------------------------------------------------------------------------
-- Maintenance mode
-- ---------------------------------------------------------------------------

-- name: ReadMaintenanceState :one
-- A read-only probe used on the hot purchase path. A missing row means
-- maintenance mode has never been engaged, which is the same as inactive.
SELECT * FROM maintenance_state WHERE singleton;

-- name: GetMaintenanceState :one
INSERT INTO maintenance_state (singleton) VALUES (true)
ON CONFLICT (singleton) DO UPDATE SET singleton = true
RETURNING *;

-- name: SetMaintenanceState :one
INSERT INTO maintenance_state (
  singleton, active, source, reason, notice_ru, notice_en, expected_return_at,
  activated_at, cleared_at
)
VALUES (
  true, sqlc.arg(active), sqlc.arg(source), sqlc.arg(reason), sqlc.arg(notice_ru),
  sqlc.arg(notice_en), sqlc.narg(expected_return_at),
  CASE WHEN sqlc.arg(active)::boolean THEN now() ELSE NULL END,
  CASE WHEN sqlc.arg(active)::boolean THEN NULL ELSE now() END
)
ON CONFLICT (singleton) DO UPDATE
SET active = EXCLUDED.active, source = EXCLUDED.source, reason = EXCLUDED.reason,
    notice_ru = EXCLUDED.notice_ru, notice_en = EXCLUDED.notice_en,
    expected_return_at = EXCLUDED.expected_return_at,
    activated_at = CASE WHEN EXCLUDED.active AND NOT maintenance_state.active THEN now()
                        WHEN EXCLUDED.active THEN maintenance_state.activated_at ELSE NULL END,
    cleared_at = CASE WHEN EXCLUDED.active THEN NULL ELSE now() END,
    updated_at = now()
RETURNING *;

-- name: InsertMaintenanceEvent :one
INSERT INTO maintenance_events (action, source, reason, actor_type, actor_id)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- ---------------------------------------------------------------------------
-- Retention and cleanup
-- ---------------------------------------------------------------------------

-- name: DeleteExpiredBotSessions :execrows
DELETE FROM bot_sessions WHERE expires_at <= now();

-- name: DeleteExpiredCheckoutSessions :execrows
DELETE FROM bot_checkout_sessions WHERE order_id IS NULL AND expires_at <= now();

-- name: DeleteExpiredWebhookEvents :execrows
DELETE FROM provider_webhook_events WHERE retain_until <= now();

-- name: DeleteExpiredSupportAttachments :execrows
DELETE FROM support_attachments WHERE retain_until <= now();

-- name: DeletePublishedOutboxEvents :execrows
DELETE FROM outbox_events
WHERE published_at IS NOT NULL
  AND published_at < now() - make_interval(secs => sqlc.arg(retention_seconds)::double precision);

-- name: DeleteOldTelemetryEvents :execrows
DELETE FROM telemetry_events
WHERE received_at < now() - make_interval(secs => sqlc.arg(retention_seconds)::double precision);

-- name: DeleteResolvedDrifts :execrows
DELETE FROM entitlement_drifts
WHERE status <> 'open'
  AND resolved_at < now() - make_interval(secs => sqlc.arg(retention_seconds)::double precision);

-- name: CountUnpublishedOutboxEvents :one
SELECT count(*)::integer AS pending_count,
       COALESCE(EXTRACT(EPOCH FROM (now() - min(occurred_at))), 0)::bigint AS oldest_age_seconds
FROM outbox_events WHERE published_at IS NULL;

-- name: UpsertGoodsCart :one
-- Saving a shop purchase for later.
--
-- It replaces whatever open cart the customer had, because there is one open
-- cart per customer and a saved plan and a saved shop item are two different
-- intentions. Silently keeping both would mean an auto-purchase charging for
-- something the customer thought they had replaced.
--
-- auto_purchase is false and stays false. A goods price is a provider quote
-- that expires; charging one unattended means charging a number the customer
-- last saw days ago.
INSERT INTO carts (
  user_id, plan_version_id, operation, currency, auto_purchase,
  idempotency_key, expires_at, kind
)
VALUES (
  sqlc.arg(user_id), NULL, 'purchase', sqlc.arg(currency), false,
  sqlc.arg(idempotency_key), sqlc.arg(expires_at), 'goods'
)
ON CONFLICT (user_id) WHERE status = 'open'
DO UPDATE SET kind = 'goods',
              plan_version_id = NULL,
              subscription_id = NULL,
              operation = 'purchase',
              currency = EXCLUDED.currency,
              promo_code = NULL,
              selected_squad_ids = '{}',
              auto_purchase = false,
              expires_at = EXCLUDED.expires_at,
              idempotency_key = EXCLUDED.idempotency_key,
              last_failure = NULL,
              attempt_count = 0,
              updated_at = now()
RETURNING *;

-- name: SetCartGoods :exec
-- The single goods line. A plan cart's add-ons are cleared alongside, because
-- a cart that changed kind must not keep the other kind's contents.
WITH cleared AS (
  DELETE FROM cart_addons WHERE cart_id = sqlc.arg(cart_id)
)
INSERT INTO cart_goods (
  cart_id, product_id, quantity, recipient_username, recipient_is_self,
  saved_price_minor, currency
)
VALUES (
  sqlc.arg(cart_id), sqlc.arg(product_id), sqlc.arg(quantity),
  sqlc.arg(recipient_username), sqlc.arg(recipient_is_self),
  sqlc.arg(saved_price_minor), sqlc.arg(currency)
)
ON CONFLICT (cart_id) DO UPDATE SET
  product_id = EXCLUDED.product_id,
  quantity = EXCLUDED.quantity,
  recipient_username = EXCLUDED.recipient_username,
  recipient_is_self = EXCLUDED.recipient_is_self,
  saved_price_minor = EXCLUDED.saved_price_minor,
  currency = EXCLUDED.currency;

-- name: GetCartGoods :one
SELECT g.*, p.code AS product_code, p.kind AS product_kind, p.visible, p.archived_at
FROM cart_goods g
JOIN goods_products p ON p.id = g.product_id
WHERE g.cart_id = $1;

-- name: ClearCartGoods :exec
DELETE FROM cart_goods WHERE cart_id = $1;

-- name: SetGoodsCartPromo :one
-- The promo code a saved shop purchase carries.
--
-- It lives on the cart rather than in a session state because the cart is
-- already the thing that survives navigating away, and a code held anywhere
-- else would be a second place a customer's intention can be lost.
UPDATE carts
SET promo_code = sqlc.narg(promo_code), updated_at = now()
WHERE user_id = sqlc.arg(user_id) AND status = 'open' AND kind = 'goods'
RETURNING *;
