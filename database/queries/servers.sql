-- The server catalogue: Omniflow's labels and sale limits over Remnawave
-- internal squads.

-- name: ListServerRules :many
-- What the catalogue says about the squads an order would grant, with the
-- price of choosing each in the order's currency.
SELECT c.squad_id, c.max_users, c.trial_eligible, p.amount_minor AS price_minor
FROM server_catalog c
LEFT JOIN server_catalog_prices p ON p.squad_id = c.squad_id AND p.currency = sqlc.arg(currency)
WHERE c.squad_id = ANY(sqlc.arg(squad_ids)::uuid[]);

-- name: CountSquadCustomers :one
-- Customers other than this one with a live entitlement on the squad.
SELECT count(DISTINCT e.user_id)::integer FROM entitlements e
WHERE e.remnawave_squad_ids @> ARRAY[sqlc.arg(squad_id)::uuid]
  AND e.status IN ('pending', 'active', 'limited', 'paused')
  AND (e.status = 'paused' OR e.ends_at > now())
  AND e.user_id <> sqlc.arg(user_id);

-- name: CustomerHoldsSquad :one
SELECT EXISTS (
  SELECT 1 FROM entitlements e
  WHERE e.user_id = sqlc.arg(user_id) AND e.remnawave_squad_ids @> ARRAY[sqlc.arg(squad_id)::uuid]
    AND e.status IN ('pending', 'active', 'limited', 'paused')
    AND (e.status = 'paused' OR e.ends_at > now())
) AS holds;

-- name: LastOrderSquads :one
-- The squads the subscription's latest settled subscription order granted,
-- which an automatic renewal keeps choosing.
SELECT o.selected_squad_ids FROM orders o
WHERE o.subscription_id = sqlc.arg(subscription_id)
  AND o.operation IN ('purchase', 'extension', 'renewal', 'upgrade', 'downgrade')
  AND o.state IN ('paid', 'fulfilled', 'partially_refunded')
ORDER BY o.created_at DESC
LIMIT 1;
