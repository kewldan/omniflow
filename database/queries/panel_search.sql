-- ---------------------------------------------------------------------------
-- Command-palette record search
--
-- One query per interpretation of the input, each an index probe, each
-- bounded by max_rows. They are separate rather than one query of ORed
-- optional predicates because an OR with "parameter IS NULL" arms cannot use
-- an index for any arm under a generic plan.
-- ---------------------------------------------------------------------------

-- name: PanelSearchCustomersByIDRange :many
SELECT u.id, u.status, u.created_at, r.telegram_id
FROM users u
LEFT JOIN remnawave_users r ON r.user_id = u.id
WHERE u.id BETWEEN sqlc.arg(low)::uuid AND sqlc.arg(high)::uuid
ORDER BY u.id
LIMIT sqlc.arg(max_rows);

-- name: PanelSearchCustomersByTelegramID :many
-- A bot customer carries the identifier on their Remnawave mapping; a web
-- customer who signed in with the Telegram widget carries it as an identity.
SELECT u.id, u.status, u.created_at, r.telegram_id
FROM users u
LEFT JOIN remnawave_users r ON r.user_id = u.id
WHERE u.id IN (
  SELECT rw.user_id FROM remnawave_users rw WHERE rw.telegram_id = sqlc.arg(telegram_id)::bigint
  UNION
  SELECT i.user_id FROM identities i
  WHERE i.provider = 'telegram' AND i.provider_subject = sqlc.arg(telegram_subject)::text
)
ORDER BY u.created_at DESC
LIMIT sqlc.arg(max_rows);

-- name: PanelSearchCustomersByEmailFingerprint :many
-- Exact match only. The address is stored encrypted beside a keyed
-- fingerprint, and the fingerprint is the only thing compared.
SELECT u.id, u.status, u.created_at, r.telegram_id
FROM users u
LEFT JOIN remnawave_users r ON r.user_id = u.id
WHERE u.id IN (
  SELECT c.user_id FROM contact_channels c
  WHERE c.kind = 'email' AND c.value_fingerprint = sqlc.arg(fingerprint) AND c.revoked_at IS NULL
)
ORDER BY u.created_at DESC
LIMIT sqlc.arg(max_rows);

-- name: PanelSearchCustomersByRemnawaveUsername :many
SELECT u.id, u.status, u.created_at, r.telegram_id
FROM users u
LEFT JOIN remnawave_users r ON r.user_id = u.id
WHERE u.id IN (
  SELECT s.user_id FROM subscriptions s
  WHERE s.remnawave_username = ANY(sqlc.arg(usernames)::text[])
)
ORDER BY u.created_at DESC
LIMIT sqlc.arg(max_rows);

-- name: PanelSearchOrdersByIDRange :many
SELECT o.id, o.user_id, o.state, o.operation, o.currency,
       (o.subtotal_minor - o.discount_minor)::bigint AS total_minor, o.created_at
FROM orders o
WHERE o.id BETWEEN sqlc.arg(low)::uuid AND sqlc.arg(high)::uuid
ORDER BY o.id
LIMIT sqlc.arg(max_rows);

-- name: PanelSearchOrdersByProviderReference :many
SELECT o.id, o.user_id, o.state, o.operation, o.currency,
       (o.subtotal_minor - o.discount_minor)::bigint AS total_minor, o.created_at,
       p.provider
FROM payment_intents p
JOIN orders o ON o.id = p.order_id
WHERE p.provider_reference = sqlc.arg(reference)::text
ORDER BY p.created_at DESC
LIMIT sqlc.arg(max_rows);

-- name: PanelSearchTicketsByIDRange :many
SELECT t.id, t.user_id, t.subject, t.status, t.last_message_at
FROM support_tickets t
WHERE t.id BETWEEN sqlc.arg(low)::uuid AND sqlc.arg(high)::uuid
ORDER BY t.id
LIMIT sqlc.arg(max_rows);

-- name: PanelSearchTicketsBySubject :many
-- The pattern arrives with its wildcards escaped; the trigram index narrows
-- the candidates and the recency order picks the ones worth showing.
SELECT t.id, t.user_id, t.subject, t.status, t.last_message_at
FROM support_tickets t
WHERE t.subject ILIKE sqlc.arg(pattern)::text
ORDER BY t.last_message_at DESC
LIMIT sqlc.arg(max_rows);

-- name: PanelSearchGiftByCodeHash :many
SELECT g.id, g.status, g.kind, g.code_hint, g.expires_at, g.sender_user_id
FROM gifts g
WHERE g.code_hash = sqlc.arg(code_hash)
LIMIT 1;

-- name: PanelSearchAccessCodeByCodeHash :many
SELECT c.id, c.status, c.code_hint, c.batch_id, b.reference AS batch_reference
FROM access_codes c
JOIN code_batches b ON b.id = c.batch_id
WHERE c.code_hash = sqlc.arg(code_hash)
LIMIT 1;
