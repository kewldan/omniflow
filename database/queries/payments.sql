
-- name: InsertPaymentQuote :exec
-- The quote is written once, when the provider confirmed the checkout. A
-- retried create finds it already there and leaves it as it was.
INSERT INTO payment_quotes (payment_intent_id, amount_minor, currency, charged_minor, charged_currency)
VALUES (sqlc.arg(payment_intent_id), sqlc.arg(amount_minor), sqlc.arg(currency),
        sqlc.arg(charged_minor), sqlc.arg(charged_currency))
ON CONFLICT (payment_intent_id) DO NOTHING;

-- name: GetPaymentQuote :one
SELECT payment_intent_id, amount_minor, currency, charged_minor, charged_currency, created_at
FROM payment_quotes
WHERE payment_intent_id = sqlc.arg(payment_intent_id);
