-- name: MarkDormantCustomers :many
-- Marks a batch of customers dormant: active accounts with no activity for
-- the configured number of days, no live or limited subscription, no money on
-- the wallet, no open support ticket, and no order still waiting for payment.
-- Anything a customer would lose or be surprised by keeps them awake.
WITH candidates AS (
  SELECT u.id
  FROM users u
  WHERE u.status = 'active'
    AND u.dormant_at IS NULL
    AND COALESCE(u.last_activity_at, u.created_at) < now() - make_interval(days => sqlc.arg(inactive_days)::int)
    AND NOT EXISTS (
      SELECT 1 FROM entitlements e
      WHERE e.user_id = u.id AND e.status IN ('active', 'limited') AND e.ends_at > now()
    )
    AND NOT EXISTS (
      SELECT 1 FROM ledger_entries l
      WHERE l.user_id = u.id AND l.account_type = 'customer_wallet'
      GROUP BY l.currency HAVING sum(l.amount_minor) <> 0
    )
    AND NOT EXISTS (SELECT 1 FROM support_tickets t WHERE t.user_id = u.id AND t.status IN ('open', 'pending'))
    AND NOT EXISTS (
      SELECT 1 FROM orders o
      WHERE o.user_id = u.id AND o.state IN ('draft', 'pending', 'paid')
    )
  ORDER BY u.created_at
  LIMIT sqlc.arg(batch_size)
  FOR UPDATE OF u SKIP LOCKED
)
UPDATE users
SET dormant_at = now(), updated_at = now()
FROM candidates
WHERE users.id = candidates.id
RETURNING users.id;

-- name: ListDormantForDeletion :many
-- Dormant customers whose dormancy has lasted the configured number of days
-- and who are still untouched: no activity since they were marked.
SELECT id FROM users
WHERE status = 'active'
  AND dormant_at IS NOT NULL
  AND dormant_at < now() - make_interval(days => sqlc.arg(dormant_days)::int)
  AND COALESCE(last_activity_at, created_at) <= dormant_at
ORDER BY dormant_at
LIMIT sqlc.arg(batch_size);

-- name: CountDormantCustomers :one
SELECT count(*)::bigint FROM users WHERE status = 'active' AND dormant_at IS NOT NULL;
