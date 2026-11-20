-- Credit lines: the unused value a change order hands back to the customer.

-- name: InsertOrderCredit :one
INSERT INTO order_credits (
  order_id, user_id, kind, source_entitlement_id, source_order_id, currency,
  basis_minor, remaining_seconds, period_seconds, credit_minor
) VALUES (
  sqlc.arg(order_id), sqlc.arg(user_id), sqlc.arg(kind), sqlc.arg(source_entitlement_id),
  sqlc.narg(source_order_id), sqlc.arg(currency), sqlc.arg(basis_minor),
  sqlc.arg(remaining_seconds), sqlc.arg(period_seconds), sqlc.arg(credit_minor)
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
