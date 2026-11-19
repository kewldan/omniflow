-- Queries behind the Remnawave synchronisation surfaces: naming new panel
-- users, webhook intake, device aliases, and the lifecycle passes.

-- name: GetCustomerTelegramID :one
SELECT (provider_subject)::bigint AS telegram_id
FROM identities
WHERE user_id = sqlc.arg(user_id) AND provider = 'telegram' AND status = 'active'
ORDER BY created_at
LIMIT 1;
