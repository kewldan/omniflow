-- Notification history.
--
-- `notification_deliveries` already recorded everything. These are the reads
-- that make it answerable — for the customer who says they never got it, and
-- for the operator who has to find out whether that is true.
--
-- `error_code` is the interesting column. It carries a transport failure when
-- one happened, and a policy outcome otherwise: `quiet_hours`, `frequency_cap`,
-- `no_consent`. "It was suppressed because you turned marketing off" is a real
-- answer; "no record" is not.

-- name: ListNotificationDeliveries :many
-- One customer's history, newest first. The subscription label comes along
-- because "your subscription expires soon" is unhelpful to somebody holding
-- three of them.
SELECT d.id, d.kind, d.class, d.status, d.scheduled_at, d.sent_at,
       d.failure_count, d.error_code, d.deferred_until,
       d.subscription_id, s.slot, COALESCE(s.label, '') AS subscription_label
FROM notification_deliveries d
LEFT JOIN subscriptions s ON s.id = d.subscription_id
WHERE d.user_id = sqlc.arg(user_id)::uuid
  AND (sqlc.narg(kind)::text IS NULL OR d.kind = sqlc.narg(kind)::text)
  AND (sqlc.narg(status)::text IS NULL OR d.status = sqlc.narg(status)::text)
ORDER BY COALESCE(d.sent_at, d.scheduled_at) DESC, d.id
LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: CountNotificationDeliveries :one
SELECT count(*) FROM notification_deliveries
WHERE user_id = sqlc.arg(user_id)::uuid
  AND (sqlc.narg(kind)::text IS NULL OR kind = sqlc.narg(kind)::text)
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text);

-- name: SummariseNotificationDeliveries :many
-- What the operator sees before reading any single row: how much of each kind
-- was sent, and how much never left for a stated reason.
SELECT kind,
       count(*) AS total,
       count(*) FILTER (WHERE status = 'sent') AS sent,
       count(*) FILTER (WHERE status = 'failed') AS failed,
       count(*) FILTER (WHERE status = 'suppressed') AS suppressed,
       count(*) FILTER (WHERE status IN ('pending', 'deferred')) AS waiting,
       max(sent_at)::timestamptz AS last_sent_at
FROM notification_deliveries
WHERE user_id = sqlc.arg(user_id)::uuid
GROUP BY kind
ORDER BY kind;

-- name: QueueTestNotification :one
-- A test is a real delivery through the real path — same table, same worker,
-- same Telegram call — under a kind of its own so it can never be counted as
-- an expiry notice or spend a marketing frequency budget. It is transactional
-- by class because it was asked for, and the dedupe key is the operator and
-- the minute, so an impatient double-click is one message rather than two.
INSERT INTO notification_deliveries (user_id, kind, dedupe_key, class, status)
VALUES (sqlc.arg(user_id)::uuid, 'test', sqlc.arg(dedupe_key), 'transactional', 'pending')
ON CONFLICT (user_id, kind, subscription_id, dedupe_key) DO UPDATE
SET status = 'pending', scheduled_at = now(), sent_at = NULL,
    error_code = NULL, deferred_until = NULL, failure_count = 0
WHERE notification_deliveries.status <> 'pending'
RETURNING id, status, scheduled_at;

-- The claim and the outcome live in `internal/botapp` beside the other delivery
-- passes rather than here: the notifier owns its own SQL, and a test travels the
-- same road as everything else it sends.
