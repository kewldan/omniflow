-- Digital goods: providers, catalogue, orders, and delivery.
--
-- Nothing in this file touches an entitlement, a subscription, or a Remnawave
-- mapping. A digital good is sold, delivered, and refunded entirely inside its
-- own tables plus the shared order, payment, and ledger pipeline.

-- ---------------------------------------------------------------------------
-- Providers
-- ---------------------------------------------------------------------------

-- name: ListGoodsProviders :many
SELECT * FROM goods_providers ORDER BY slug;

-- name: GetGoodsProvider :one
SELECT * FROM goods_providers WHERE slug = $1;

-- name: UpsertGoodsProvider :one
-- A null ciphertext on update means "leave the stored credential alone", so the
-- panel can render and re-save the form without ever echoing a secret back.
INSERT INTO goods_providers (
  slug, enabled, credentials_ciphertext, low_balance_threshold_minor,
  spend_limit_minor, spend_window_seconds
) VALUES (
  sqlc.arg(slug), sqlc.arg(enabled), sqlc.narg(credentials_ciphertext),
  sqlc.narg(low_balance_threshold_minor), sqlc.arg(spend_limit_minor),
  sqlc.arg(spend_window_seconds)
)
ON CONFLICT (slug) DO UPDATE
SET enabled = EXCLUDED.enabled,
    credentials_ciphertext = COALESCE(
      EXCLUDED.credentials_ciphertext, goods_providers.credentials_ciphertext
    ),
    low_balance_threshold_minor = EXCLUDED.low_balance_threshold_minor,
    spend_limit_minor = EXCLUDED.spend_limit_minor,
    spend_window_seconds = EXCLUDED.spend_window_seconds,
    updated_at = now()
RETURNING *;

-- name: RecordGoodsProviderHealth :one
UPDATE goods_providers
SET status = sqlc.arg(status),
    last_error_code = sqlc.narg(last_error_code),
    balance_minor = sqlc.narg(balance_minor),
    balance_currency = sqlc.narg(balance_currency),
    last_checked_at = now(),
    updated_at = now()
WHERE slug = sqlc.arg(slug)
RETURNING *;

-- name: SumRecentGoodsSpend :one
-- Provider cost, not customer price: the spend ceiling protects the operator's
-- funding source, and that is what the provider actually draws down.
SELECT COALESCE(sum(g.quoted_cost_minor), 0)::bigint
FROM goods_orders g
JOIN goods_deliveries d ON d.order_id = g.order_id
JOIN goods_products p ON p.id = g.product_id
WHERE p.provider_slug = sqlc.arg(provider_slug)
  AND d.status IN ('submitted', 'delivered')
  AND d.created_at >= now() - sqlc.arg(lookback)::interval;

-- ---------------------------------------------------------------------------
-- Catalogue
-- ---------------------------------------------------------------------------

-- name: ListGoodsProducts :many
SELECT * FROM goods_products
WHERE (sqlc.narg(include_archived)::boolean IS TRUE OR archived_at IS NULL)
ORDER BY sort_order, code;

-- name: ListVisibleGoodsProducts :many
-- The customer-facing catalogue in one round trip: product, localisation for
-- the requested locale, and the pricing rule that produces the quote.
SELECT sqlc.embed(p), sqlc.embed(l), sqlc.embed(c)
FROM goods_products p
JOIN goods_product_localizations l ON l.product_id = p.id AND l.locale = sqlc.arg(locale)
JOIN goods_pricing c ON c.product_id = p.id
JOIN goods_providers v ON v.slug = p.provider_slug AND v.enabled
WHERE p.visible AND p.archived_at IS NULL
ORDER BY p.sort_order, p.code;

-- name: GetGoodsProduct :one
SELECT * FROM goods_products WHERE id = $1;

-- name: GetGoodsProductByCode :one
SELECT * FROM goods_products WHERE code = $1;

-- name: CreateGoodsProduct :one
INSERT INTO goods_products (
  code, provider_slug, kind, duration_months, star_quantity, visible, sort_order
) VALUES (
  sqlc.arg(code), sqlc.arg(provider_slug), sqlc.arg(kind),
  sqlc.narg(duration_months), sqlc.narg(star_quantity),
  sqlc.arg(visible), sqlc.arg(sort_order)
)
ON CONFLICT (code) DO NOTHING
RETURNING *;

-- name: UpdateGoodsProduct :one
UPDATE goods_products
SET visible = sqlc.arg(visible),
    sort_order = sqlc.arg(sort_order),
    archived_at = CASE WHEN sqlc.arg(archived)::boolean THEN COALESCE(archived_at, now()) ELSE NULL END
WHERE id = sqlc.arg(product_id)
RETURNING *;

-- name: UpsertGoodsProductLocalization :one
INSERT INTO goods_product_localizations (product_id, locale, name, description)
VALUES (sqlc.arg(product_id), sqlc.arg(locale), sqlc.arg(name), sqlc.arg(description))
ON CONFLICT (product_id, locale) DO UPDATE
SET name = EXCLUDED.name, description = EXCLUDED.description
RETURNING *;

-- name: ListGoodsProductLocalizations :many
SELECT * FROM goods_product_localizations WHERE product_id = $1 ORDER BY locale;

-- name: GetGoodsPricing :one
SELECT * FROM goods_pricing WHERE product_id = $1;

-- name: UpsertGoodsPricing :one
INSERT INTO goods_pricing (
  product_id, currency, markup_bps, rounding, fixed_amount_minor, quote_ttl_seconds, updated_by
) VALUES (
  sqlc.arg(product_id), sqlc.arg(currency), sqlc.arg(markup_bps), sqlc.arg(rounding),
  sqlc.narg(fixed_amount_minor), sqlc.arg(quote_ttl_seconds), sqlc.narg(updated_by)
)
ON CONFLICT (product_id) DO UPDATE
SET currency = EXCLUDED.currency,
    markup_bps = EXCLUDED.markup_bps,
    rounding = EXCLUDED.rounding,
    fixed_amount_minor = EXCLUDED.fixed_amount_minor,
    quote_ttl_seconds = EXCLUDED.quote_ttl_seconds,
    updated_by = EXCLUDED.updated_by,
    updated_at = now()
RETURNING *;

-- ---------------------------------------------------------------------------
-- Orders
-- ---------------------------------------------------------------------------

-- name: CreateGoodsOrder :one
INSERT INTO goods_orders (
  order_id, user_id, product_id, quantity, recipient_username, recipient_is_self,
  quoted_cost_minor, quoted_price_minor, currency, quote_expires_at
) VALUES (
  sqlc.arg(order_id), sqlc.arg(user_id), sqlc.arg(product_id), sqlc.arg(quantity),
  sqlc.arg(recipient_username), sqlc.arg(recipient_is_self),
  sqlc.arg(quoted_cost_minor), sqlc.arg(quoted_price_minor), sqlc.arg(currency),
  sqlc.arg(quote_expires_at)
)
ON CONFLICT (order_id) DO NOTHING
RETURNING *;

-- name: GetGoodsOrder :one
SELECT * FROM goods_orders WHERE order_id = $1;

-- name: SetGoodsOrderStatus :one
UPDATE goods_orders
SET status = sqlc.arg(status), updated_at = now()
WHERE order_id = sqlc.arg(order_id)
RETURNING *;

-- name: ListGoodsOrdersForCustomer :many
SELECT sqlc.embed(g), sqlc.embed(p)
FROM goods_orders g
JOIN goods_products p ON p.id = g.product_id
WHERE g.user_id = $1
ORDER BY g.created_at DESC
LIMIT sqlc.arg(page_size);

-- name: SearchGoodsOrders :many
-- Delivery columns are selected individually because an order that was never
-- paid has no delivery row, and an embedded struct cannot hold that absence.
SELECT
  sqlc.embed(g),
  d.status AS delivery_status,
  d.attempt_count AS delivery_attempts,
  d.failure_class AS delivery_failure_class,
  d.last_error_code AS delivery_error_code,
  d.delivered_at,
  d.refund_ledger_transaction_id
FROM goods_orders g
LEFT JOIN goods_deliveries d ON d.order_id = g.order_id
WHERE (
    sqlc.narg(cursor_created_at)::timestamptz IS NULL
    OR (g.created_at, g.order_id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
  AND (sqlc.narg(status)::text IS NULL OR g.status = sqlc.narg(status))
  AND (sqlc.narg(user_id)::uuid IS NULL OR g.user_id = sqlc.narg(user_id))
ORDER BY g.created_at DESC, g.order_id DESC
LIMIT sqlc.arg(page_size);

-- ---------------------------------------------------------------------------
-- Delivery
-- ---------------------------------------------------------------------------

-- name: CreateGoodsDelivery :one
-- The primary key on `order_id` is the whole double-delivery guard: a second
-- insert for the same order conflicts and returns nothing, whatever caused the
-- replay.
INSERT INTO goods_deliveries (order_id, provider_slug, idempotency_key)
VALUES (sqlc.arg(order_id), sqlc.arg(provider_slug), sqlc.arg(idempotency_key))
ON CONFLICT (order_id) DO NOTHING
RETURNING *;

-- name: GetGoodsDelivery :one
SELECT * FROM goods_deliveries WHERE order_id = $1;

-- name: LockGoodsDelivery :one
-- Taken before every provider submission. Two workers racing on one order
-- serialise here, and the loser re-reads a row that is already submitted.
SELECT * FROM goods_deliveries WHERE order_id = $1 FOR UPDATE;

-- name: ListDueGoodsDeliveries :many
SELECT * FROM goods_deliveries
WHERE status IN ('pending', 'submitted') AND next_attempt_at <= now()
ORDER BY next_attempt_at
LIMIT sqlc.arg(page_size);

-- name: BeginGoodsDeliveryAttempt :one
-- Claims the next attempt and pushes the retry deadline out before any provider
-- call is made, so a worker that dies mid-request does not have the row picked
-- up again immediately.
UPDATE goods_deliveries
SET attempt_count = attempt_count + 1,
    next_attempt_at = now() + sqlc.arg(backoff)::interval,
    status = 'submitted',
    updated_at = now()
WHERE order_id = sqlc.arg(order_id) AND status IN ('pending', 'submitted')
RETURNING *;

-- name: CompleteGoodsDelivery :one
UPDATE goods_deliveries
SET status = 'delivered',
    provider_reference = sqlc.narg(provider_reference),
    failure_class = NULL,
    last_error_code = NULL,
    delivered_at = now(),
    updated_at = now()
WHERE order_id = sqlc.arg(order_id) AND status <> 'delivered'
RETURNING *;

-- name: FailGoodsDelivery :one
UPDATE goods_deliveries
SET status = CASE WHEN sqlc.arg(terminal)::boolean THEN 'failed' ELSE 'submitted' END,
    failure_class = sqlc.arg(failure_class),
    last_error_code = sqlc.narg(last_error_code),
    next_attempt_at = now() + sqlc.arg(backoff)::interval,
    updated_at = now()
WHERE order_id = sqlc.arg(order_id) AND status <> 'delivered'
RETURNING *;

-- name: RecordGoodsDeliveryRefund :one
UPDATE goods_deliveries
SET refund_ledger_transaction_id = sqlc.arg(ledger_transaction_id), updated_at = now()
WHERE order_id = sqlc.arg(order_id) AND refund_ledger_transaction_id IS NULL
RETURNING *;

-- name: CancelGoodsDelivery :one
UPDATE goods_deliveries
SET status = 'cancelled', updated_at = now()
WHERE order_id = sqlc.arg(order_id) AND status IN ('pending', 'submitted')
RETURNING *;

-- name: InsertGoodsDeliveryAttempt :one
INSERT INTO goods_delivery_attempts (
  order_id, attempt, outcome, failure_class, error_code, provider_reference, correlation_id
) VALUES (
  sqlc.arg(order_id), sqlc.arg(attempt), sqlc.arg(outcome), sqlc.narg(failure_class),
  sqlc.narg(error_code), sqlc.narg(provider_reference), sqlc.arg(correlation_id)
)
ON CONFLICT (order_id, attempt) DO NOTHING
RETURNING *;

-- name: ListGoodsDeliveryAttempts :many
SELECT * FROM goods_delivery_attempts WHERE order_id = $1 ORDER BY attempt;
