-- Credit lines: the unused value a change order hands back to the customer.

-- name: InsertOrderCredit :one
INSERT INTO order_credits (
  order_id, user_id, kind, source_entitlement_id, source_order_id, currency,
  basis_minor, remaining_seconds, period_seconds, credit_minor, released_addon_ids
) VALUES (
  sqlc.arg(order_id), sqlc.arg(user_id), sqlc.arg(kind), sqlc.arg(source_entitlement_id),
  sqlc.narg(source_order_id), sqlc.arg(currency), sqlc.arg(basis_minor),
  sqlc.arg(remaining_seconds), sqlc.arg(period_seconds), sqlc.arg(credit_minor),
  sqlc.arg(released_addon_ids)::uuid[]
)
RETURNING *;

-- name: GetOrderCredit :one
SELECT * FROM order_credits WHERE order_id = sqlc.arg(order_id);

-- name: LockOrderCredit :one
SELECT * FROM order_credits WHERE order_id = sqlc.arg(order_id) FOR UPDATE;

-- name: MarkOrderCreditPosted :one
-- The predicate makes a replayed settlement match nothing, so a credit line is
-- posted once.
UPDATE order_credits
SET ledger_transaction_id = sqlc.arg(ledger_transaction_id), credited_at = now()
WHERE order_id = sqlc.arg(order_id) AND credited_at IS NULL
RETURNING *;

-- name: PlanRemainderPosted :one
-- Whether the remainder of an entitlement was already handed back by another
-- order.
SELECT EXISTS (
  SELECT 1 FROM order_credits
  WHERE source_entitlement_id = sqlc.arg(source_entitlement_id)
    AND kind = 'plan_remainder' AND credited_at IS NOT NULL
)::boolean;

-- name: GetRemainderBasis :one
-- What the order behind an entitlement paid, and the period that payment
-- bought. Only the entitlement's own customer's paid plan orders count: a
-- gift, a code, a trial and an operator's grant paid nothing the holder could
-- be handed back.
SELECT o.id AS order_id, o.user_id, o.operation, o.currency,
       (o.wallet_minor + o.external_minor - o.refunded_minor)::bigint AS paid_minor,
       COALESCE(o.period_seconds, pv.duration_seconds)::bigint AS period_seconds
FROM orders o
JOIN order_lines ol ON ol.order_id = o.id
JOIN plan_versions pv ON pv.id = ol.plan_version_id
WHERE o.id = sqlc.arg(order_id)
LIMIT 1;

-- name: ListLivePacks :many
-- The add-on packs still counting on an entitlement, newest first, with what
-- the order that bought each one took for it.
SELECT ea.id, ea.entitlement_id, ea.order_id, ea.addon_version_id, ea.quantity,
       ea.traffic_bytes, ea.device_slots, ea.remnawave_squad_ids, ea.applied_at, ea.expires_at,
       a.kind AS addon_kind, a.code AS addon_code,
       COALESCE(ol.charged_minor, 0)::bigint AS charged_minor,
       o.user_id AS order_user_id, o.currency AS order_currency,
       o.subtotal_minor AS order_subtotal_minor,
       (o.wallet_minor + o.external_minor - o.refunded_minor)::bigint AS order_paid_minor
FROM entitlement_addons ea
JOIN addon_versions av ON av.id = ea.addon_version_id
JOIN addons a ON a.id = av.addon_id
JOIN orders o ON o.id = ea.order_id
LEFT JOIN order_addon_lines ol ON ol.order_id = ea.order_id AND ol.addon_version_id = ea.addon_version_id
WHERE ea.entitlement_id = sqlc.arg(entitlement_id)
  AND ea.released_at IS NULL AND ea.expired_at IS NULL
ORDER BY ea.applied_at DESC, ea.id DESC;

-- name: ReleaseEntitlementAddon :one
-- Takes one pack off its entitlement for a change order. The predicate makes a
-- replayed settlement, or a second change racing for the same pack, match
-- nothing, so its capacity is subtracted once.
UPDATE entitlement_addons
SET released_at = now(), released_by_order_id = sqlc.arg(order_id)
WHERE id = sqlc.arg(entitlement_addon_id) AND released_at IS NULL AND expired_at IS NULL
RETURNING *;

-- name: CountReleasablePacks :one
-- How many of the given packs are still on the entitlement and unreleased.
SELECT count(*)::int FROM entitlement_addons
WHERE id = ANY(sqlc.arg(entitlement_addon_ids)::uuid[])
  AND entitlement_id = sqlc.arg(entitlement_id)
  AND released_at IS NULL AND expired_at IS NULL;
