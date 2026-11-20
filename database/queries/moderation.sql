-- Moderation: bans by list, the bot-blocker scan, and cleaning blockers up.

-- name: ResolveCustomerByTelegramID :one
SELECT user_id FROM (
  SELECT i.user_id FROM identities i
  WHERE i.provider = 'telegram' AND i.status = 'active' AND i.provider_subject = sqlc.arg(telegram_id)::bigint::text
  UNION
  SELECT r.user_id FROM remnawave_users r WHERE r.telegram_id = sqlc.arg(telegram_id)::bigint
) found
LIMIT 1;

-- name: RequestBlockerScan :one
INSERT INTO blocker_scans (requested_by) VALUES (sqlc.narg(requested_by)) RETURNING *;

-- name: LatestBlockerScan :one
SELECT * FROM blocker_scans ORDER BY requested_at DESC LIMIT 1;

-- name: ClaimBlockerScan :one
UPDATE blocker_scans SET status = 'running', started_at = now()
WHERE id = (SELECT id FROM blocker_scans WHERE status = 'pending' ORDER BY requested_at LIMIT 1 FOR UPDATE SKIP LOCKED)
RETURNING *;

-- name: ProgressBlockerScan :exec
UPDATE blocker_scans SET checked = sqlc.arg(checked), blocked = sqlc.arg(blocked)
WHERE id = sqlc.arg(scan_id);

-- name: FinishBlockerScan :exec
UPDATE blocker_scans SET status = sqlc.arg(status), finished_at = now() WHERE id = sqlc.arg(scan_id);

-- name: CountBlockedCustomers :one
SELECT count(*)::bigint FROM bot_delivery_state d
JOIN users u ON u.id = d.user_id
WHERE d.status = 'blocked' AND u.status = 'active';

-- name: ListCleanableBlockers :many
-- Customers who blocked the bot and hold no live access: the ones a clean-up
-- suspends. Anybody with a running subscription is left alone — they pay for
-- a VPN, not for the bot.
SELECT d.user_id FROM bot_delivery_state d
JOIN users u ON u.id = d.user_id
WHERE d.status = 'blocked' AND u.status = 'active'
  AND NOT EXISTS (
    SELECT 1 FROM entitlements e WHERE e.user_id = d.user_id
      AND e.status IN ('active', 'limited', 'paused', 'pending') AND e.ends_at > now())
LIMIT sqlc.arg(page_size);

-- name: CloseAllSupportTickets :execrows
-- Closes every open or pending ticket, of one queue or of all.
UPDATE support_tickets
SET status = 'closed', closed_at = now(), updated_at = now()
WHERE status IN ('open', 'pending')
  AND (sqlc.narg(queue_id)::uuid IS NULL OR queue_id = sqlc.narg(queue_id)::uuid);

-- name: DeleteSupportMessage :execrows
UPDATE support_messages
SET deleted_at = now(), deleted_by = sqlc.narg(deleted_by)
WHERE id = sqlc.arg(message_id) AND ticket_id = sqlc.arg(ticket_id) AND deleted_at IS NULL;
