-- Operator notification delivery that honours the panel's switches, and the
-- application error log.

-- name: ListDueOperatorNotifications :many
-- Pending notices except the kinds held back by quiet hours. Held rows stay
-- pending and are delivered when the quiet window ends; excluding them here
-- rather than skipping them in Go keeps them from filling every batch.
SELECT * FROM operator_notifications
WHERE status = 'pending'
  AND NOT (kind = ANY (sqlc.arg(held_kinds)::text[]))
ORDER BY created_at
LIMIT sqlc.arg(page_size)
FOR UPDATE SKIP LOCKED;

-- name: EnqueueOperatorNotice :one
-- Queues a notice from a process that is not inside another transaction.
INSERT INTO operator_notifications (kind, dedupe_key, payload)
VALUES (sqlc.arg(kind), sqlc.arg(dedupe_key), sqlc.arg(payload))
ON CONFLICT (kind, dedupe_key) DO NOTHING
RETURNING id;

-- name: RecordAppError :one
-- One row per fingerprint. A recurrence bumps the count and the last-seen
-- time and reopens a resolved row, because an error that came back is news.
INSERT INTO app_errors (fingerprint, process, message, source)
VALUES (sqlc.arg(fingerprint), sqlc.arg(process), sqlc.arg(message), sqlc.arg(source))
ON CONFLICT (fingerprint) DO UPDATE
SET occurrences = app_errors.occurrences + 1,
    last_seen_at = now(),
    resolved_at = NULL
RETURNING id, occurrences, (xmax = 0)::boolean AS inserted, (resolved_at IS NULL)::boolean AS open;

-- name: ListAppErrors :many
SELECT * FROM app_errors
WHERE (sqlc.arg(include_resolved)::boolean OR resolved_at IS NULL)
  AND (sqlc.narg(process)::text IS NULL OR process = sqlc.narg(process)::text)
ORDER BY last_seen_at DESC
LIMIT sqlc.arg(page_size);

-- name: ResolveAppError :one
UPDATE app_errors SET resolved_at = now()
WHERE id = sqlc.arg(error_id) AND resolved_at IS NULL
RETURNING *;

-- name: ResolveAllAppErrors :execrows
UPDATE app_errors SET resolved_at = now() WHERE resolved_at IS NULL;

-- name: QueueSupportSLABreaches :execrows
-- One support notice per ticket whose first response is overdue. The dedupe
-- key is the ticket, so a breach is announced once however long it lasts.
INSERT INTO operator_notifications (kind, dedupe_key, payload)
SELECT 'support', 'sla:' || t.id::text,
       jsonb_build_object(
         'ticketId', t.id::text, 'queueId', t.queue_id::text, 'event', 'sla_breach',
         'priority', t.priority,
         'overdueMinutes', floor(extract(epoch FROM now() - t.created_at
           - make_interval(secs => q.first_response_target_seconds)) / 60)::bigint)
FROM support_tickets t
JOIN support_queues q ON q.id = t.queue_id
WHERE t.status IN ('open', 'pending')
  AND q.first_response_target_seconds > 0
  AND t.first_response_at IS NULL
  AND t.created_at < now() - make_interval(secs => q.first_response_target_seconds)
ON CONFLICT (kind, dedupe_key) DO NOTHING;

-- name: OperatorReportFigures :one
-- The numbers a scheduled report carries. Money is per currency, so revenue
-- comes back as a JSON object keyed by currency.
SELECT
  (SELECT count(*) FROM users nu WHERE nu.created_at >= sqlc.arg(window_start)::timestamptz AND nu.created_at < sqlc.arg(window_end)::timestamptz)::bigint AS new_customers,
  (SELECT count(*) FROM trial_claims WHERE claimed_at >= sqlc.arg(window_start) AND claimed_at < sqlc.arg(window_end))::bigint AS trials,
  (SELECT count(*) FROM orders WHERE paid_at >= sqlc.arg(window_start) AND paid_at < sqlc.arg(window_end)
     AND paid_minor > 0 AND operation <> 'topup')::bigint AS paid_orders,
  (SELECT count(*) FROM orders WHERE paid_at >= sqlc.arg(window_start) AND paid_at < sqlc.arg(window_end)
     AND operation = 'topup')::bigint AS topups,
  COALESCE((SELECT jsonb_object_agg(currency, total) FROM (
     SELECT currency, sum(paid_minor)::bigint AS total FROM orders
     WHERE paid_at >= sqlc.arg(window_start) AND paid_at < sqlc.arg(window_end) AND paid_minor > 0
     GROUP BY currency) revenue), '{}'::jsonb)::jsonb AS revenue,
  (SELECT count(*) FROM support_tickets st WHERE st.created_at >= sqlc.arg(window_start)::timestamptz AND st.created_at < sqlc.arg(window_end)::timestamptz)::bigint AS tickets_opened,
  (SELECT count(*) FROM support_tickets WHERE status IN ('open', 'pending'))::bigint AS tickets_waiting,
  (SELECT count(*) FROM entitlements WHERE status = 'active')::bigint AS active_entitlements;
