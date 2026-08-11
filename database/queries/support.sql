-- The support desk.
--
-- Every read here is queue-shaped: an operator's question is almost always
-- "what is waiting, and who has it", not "show me ticket X". The one lookup by
-- identifier exists for the conversation view a queue row links to.

-- ---------------------------------------------------------------------------
-- Queues
-- ---------------------------------------------------------------------------

-- name: ListSupportQueues :many
SELECT
  sqlc.embed(q),
  (SELECT count(*)::bigint FROM support_tickets t
    WHERE t.queue_id = q.id AND t.status IN ('open', 'pending')) AS open_count,
  (SELECT count(*)::bigint FROM support_tickets t
    WHERE t.queue_id = q.id AND t.status IN ('open', 'pending') AND t.assignee_id IS NULL)
    AS unassigned_count,
  (SELECT count(*)::bigint FROM support_tickets t
    WHERE t.queue_id = q.id AND t.status IN ('open', 'pending')
      AND q.first_response_target_seconds > 0
      AND t.first_response_at IS NULL
      AND t.created_at < now() - make_interval(secs => q.first_response_target_seconds))
    AS breached_count
FROM support_queues q
WHERE q.archived_at IS NULL
ORDER BY q.sort_order, q.code;

-- name: GetSupportQueue :one
SELECT * FROM support_queues WHERE id = $1;

-- name: GetDefaultSupportQueue :one
SELECT * FROM support_queues WHERE is_default AND archived_at IS NULL;

-- name: UpsertSupportQueue :one
INSERT INTO support_queues (
  code, name_en, name_ru, first_response_target_seconds, resolution_target_seconds, sort_order
) VALUES (
  sqlc.arg(code), sqlc.arg(name_en), sqlc.arg(name_ru),
  sqlc.arg(first_response_target_seconds), sqlc.arg(resolution_target_seconds),
  sqlc.arg(sort_order)
)
ON CONFLICT (code) DO UPDATE SET
  name_en = EXCLUDED.name_en, name_ru = EXCLUDED.name_ru,
  first_response_target_seconds = EXCLUDED.first_response_target_seconds,
  resolution_target_seconds = EXCLUDED.resolution_target_seconds,
  sort_order = EXCLUDED.sort_order, archived_at = NULL, updated_at = now()
RETURNING *;

-- name: SetDefaultSupportQueue :exec
-- Clearing and setting run in one transaction, because the partial unique index
-- allows exactly one default: doing them apart would leave a window in which a
-- new ticket has nowhere to go.
UPDATE support_queues
SET is_default = (support_queues.id = sqlc.arg(queue_id)::uuid), updated_at = now()
WHERE archived_at IS NULL
  AND (is_default OR support_queues.id = sqlc.arg(queue_id)::uuid);

-- name: ArchiveSupportQueue :one
-- A queue with open tickets in it cannot be archived. Archiving one would hide
-- work rather than finish it.
UPDATE support_queues q
SET archived_at = now(), is_default = false, updated_at = now()
WHERE q.id = sqlc.arg(queue_id)
  AND q.archived_at IS NULL
  AND NOT q.is_default
  AND NOT EXISTS (
    SELECT 1 FROM support_tickets t
    WHERE t.queue_id = q.id AND t.status IN ('open', 'pending')
  )
RETURNING *;

-- ---------------------------------------------------------------------------
-- Ticket queue view
-- ---------------------------------------------------------------------------

-- name: SearchSupportTickets :many
-- The queue. Oldest activity first, because a desk works the top of the list.
--
-- The SLA columns are computed here rather than in the panel so that "overdue"
-- means the same thing in the list, in the counters, and in the report.
SELECT
  sqlc.embed(t),
  q.code AS queue_code,
  q.first_response_target_seconds,
  q.resolution_target_seconds,
  COALESCE(a.display_name, '') AS assignee_name,
  u.status AS customer_status,
  (q.first_response_target_seconds > 0
    AND t.first_response_at IS NULL
    AND t.created_at < now() - make_interval(secs => q.first_response_target_seconds))
    AS first_response_breached,
  (SELECT count(*)::bigint FROM support_messages m WHERE m.ticket_id = t.id) AS message_count,
  ARRAY(
    SELECT g.code FROM support_ticket_tags tt
    JOIN support_tags g ON g.id = tt.tag_id
    WHERE tt.ticket_id = t.id
    ORDER BY g.code
  )::text[] AS tags
FROM support_tickets t
JOIN support_queues q ON q.id = t.queue_id
JOIN users u ON u.id = t.user_id
LEFT JOIN admin_users a ON a.id = t.assignee_id
WHERE (sqlc.narg(queue_id)::uuid IS NULL OR t.queue_id = sqlc.narg(queue_id)::uuid)
  AND (sqlc.narg(status)::text IS NULL OR t.status = sqlc.narg(status)::text)
  AND (sqlc.narg(priority)::text IS NULL OR t.priority = sqlc.narg(priority)::text)
  AND (sqlc.narg(assignee_id)::uuid IS NULL OR t.assignee_id = sqlc.narg(assignee_id)::uuid)
  AND (NOT sqlc.arg(unassigned_only)::boolean OR t.assignee_id IS NULL)
  AND (sqlc.narg(customer_id)::uuid IS NULL OR t.user_id = sqlc.narg(customer_id)::uuid)
  AND (sqlc.narg(tag)::text IS NULL OR EXISTS (
    SELECT 1 FROM support_ticket_tags tt
    JOIN support_tags g ON g.id = tt.tag_id
    WHERE tt.ticket_id = t.id AND g.code = sqlc.narg(tag)::text
  ))
  AND (
    sqlc.narg(cursor_last_message_at)::timestamptz IS NULL
    OR (t.last_message_at, t.id) > (sqlc.narg(cursor_last_message_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
ORDER BY t.last_message_at, t.id
LIMIT sqlc.arg(page_size);

-- name: GetSupportTicket :one
SELECT
  sqlc.embed(t),
  q.code AS queue_code,
  q.first_response_target_seconds,
  q.resolution_target_seconds,
  COALESCE(a.display_name, '') AS assignee_name,
  u.status AS customer_status,
  ARRAY(
    SELECT g.code FROM support_ticket_tags tt
    JOIN support_tags g ON g.id = tt.tag_id
    WHERE tt.ticket_id = t.id ORDER BY g.code
  )::text[] AS tags
FROM support_tickets t
JOIN support_queues q ON q.id = t.queue_id
JOIN users u ON u.id = t.user_id
LEFT JOIN admin_users a ON a.id = t.assignee_id
WHERE t.id = $1;

-- name: LockSupportTicket :one
SELECT * FROM support_tickets WHERE id = $1 FOR UPDATE;

-- ---------------------------------------------------------------------------
-- Ticket mutations
-- ---------------------------------------------------------------------------

-- name: AssignSupportTicket :one
-- Assigning to NULL releases the ticket back to its queue, which is a normal
-- action rather than an error state: an operator who cannot finish something
-- should be able to put it down.
UPDATE support_tickets
SET assignee_id = sqlc.narg(assignee_id),
    assigned_at = CASE WHEN sqlc.narg(assignee_id)::uuid IS NULL THEN NULL ELSE now() END,
    updated_at = now()
WHERE id = sqlc.arg(ticket_id) AND status <> 'merged'
RETURNING *;

-- name: MoveSupportTicket :one
UPDATE support_tickets
SET queue_id = sqlc.arg(queue_id), updated_at = now()
WHERE id = sqlc.arg(ticket_id) AND status <> 'merged'
RETURNING *;

-- name: SetSupportTicketPriority :one
UPDATE support_tickets
SET priority = sqlc.arg(priority), updated_at = now()
WHERE id = sqlc.arg(ticket_id) AND status <> 'merged'
RETURNING *;

-- name: SetSupportTicketStatus :one
-- Resolution and reopening are recorded rather than inferred.
--
-- `resolved_at` is set the first time a ticket reaches a terminal state and
-- cleared when it comes back, and `reopened_count` counts the round trips —
-- which is the signal that an answer did not actually answer the question.
UPDATE support_tickets
SET status = sqlc.arg(status),
    resolved_at = CASE
      WHEN sqlc.arg(status)::text IN ('resolved', 'closed') THEN COALESCE(resolved_at, now())
      ELSE NULL
    END,
    closed_at = CASE WHEN sqlc.arg(status)::text = 'closed' THEN now() ELSE NULL END,
    reopened_count = CASE
      WHEN sqlc.arg(status)::text IN ('open', 'pending') AND resolved_at IS NOT NULL
        THEN reopened_count + 1
      ELSE reopened_count
    END,
    updated_at = now()
WHERE id = sqlc.arg(ticket_id) AND status <> 'merged'
RETURNING *;

-- name: MergeSupportTicket :one
-- The absorbed ticket keeps its row and its messages and points at its
-- survivor. Deleting it would lose the customer's own words and the trail that
-- explains where they went.
UPDATE support_tickets
SET status = 'merged',
    merged_into_ticket_id = sqlc.arg(survivor_id),
    resolved_at = COALESCE(resolved_at, now()),
    updated_at = now()
WHERE id = sqlc.arg(ticket_id)
  AND status <> 'merged'
  AND id <> sqlc.arg(survivor_id)
RETURNING *;

-- name: MoveSupportMessages :exec
-- Moves the absorbed ticket's messages onto the survivor so the conversation
-- reads as one thread.
UPDATE support_messages
SET ticket_id = sqlc.arg(survivor_id)
WHERE ticket_id = sqlc.arg(ticket_id);

-- name: MarkSupportTicketRead :one
UPDATE support_tickets SET operator_unread_count = 0, updated_at = now()
WHERE id = sqlc.arg(ticket_id)
RETURNING *;

-- name: RecordSupportFirstResponse :one
-- Only the first operator reply sets it, so the measure survives a
-- conversation that goes back and forth for a week.
UPDATE support_tickets
SET first_response_at = now(), updated_at = now()
WHERE id = sqlc.arg(ticket_id) AND first_response_at IS NULL
RETURNING *;

-- ---------------------------------------------------------------------------
-- Messages, notes, and tags
-- ---------------------------------------------------------------------------

-- name: ListSupportMessages :many
SELECT sqlc.embed(m), COALESCE(a.display_name, '') AS author_name
FROM support_messages m
LEFT JOIN admin_users a ON a.id = m.author_id
WHERE m.ticket_id = $1
ORDER BY m.created_at
LIMIT sqlc.arg(page_size);

-- name: AppendOperatorMessage :one
INSERT INTO support_messages (ticket_id, sender, body, author_id, canned_response_id, dedupe_key)
VALUES (
  sqlc.arg(ticket_id), 'operator', sqlc.arg(body), sqlc.arg(author_id),
  sqlc.narg(canned_response_id), sqlc.arg(dedupe_key)
)
ON CONFLICT (ticket_id, dedupe_key) WHERE dedupe_key IS NOT NULL DO NOTHING
RETURNING *;

-- name: ListSupportNotes :many
SELECT sqlc.embed(n), COALESCE(a.display_name, '') AS author_name
FROM support_notes n
JOIN admin_users a ON a.id = n.author_id
WHERE n.ticket_id = $1
ORDER BY n.created_at
LIMIT sqlc.arg(page_size);

-- name: AppendSupportNote :one
INSERT INTO support_notes (ticket_id, author_id, body)
VALUES (sqlc.arg(ticket_id), sqlc.arg(author_id), sqlc.arg(body))
RETURNING *;

-- name: ListSupportTags :many
SELECT * FROM support_tags WHERE archived_at IS NULL ORDER BY code;

-- name: UpsertSupportTag :one
INSERT INTO support_tags (code, name_en, name_ru)
VALUES (sqlc.arg(code), sqlc.arg(name_en), sqlc.arg(name_ru))
ON CONFLICT (code) DO UPDATE SET
  name_en = EXCLUDED.name_en, name_ru = EXCLUDED.name_ru, archived_at = NULL
RETURNING *;

-- name: TagSupportTicket :exec
INSERT INTO support_ticket_tags (ticket_id, tag_id, tagged_by)
VALUES (sqlc.arg(ticket_id), sqlc.arg(tag_id), sqlc.narg(tagged_by))
ON CONFLICT DO NOTHING;

-- name: UntagSupportTicket :exec
DELETE FROM support_ticket_tags
WHERE ticket_id = sqlc.arg(ticket_id) AND tag_id = sqlc.arg(tag_id);

-- name: GetSupportTagByCode :one
SELECT * FROM support_tags WHERE code = $1;

-- ---------------------------------------------------------------------------
-- Canned responses
-- ---------------------------------------------------------------------------

-- name: ListCannedResponses :many
SELECT * FROM support_canned_responses
WHERE archived_at IS NULL
ORDER BY usage_count DESC, code;

-- name: GetCannedResponse :one
SELECT * FROM support_canned_responses WHERE id = $1 AND archived_at IS NULL;

-- name: UpsertCannedResponse :one
INSERT INTO support_canned_responses (
  code, title_en, title_ru, body_en, body_ru, requires_permission, updated_by
) VALUES (
  sqlc.arg(code), sqlc.arg(title_en), sqlc.arg(title_ru),
  sqlc.arg(body_en), sqlc.arg(body_ru), sqlc.arg(requires_permission), sqlc.narg(updated_by)
)
ON CONFLICT (code) DO UPDATE SET
  title_en = EXCLUDED.title_en, title_ru = EXCLUDED.title_ru,
  body_en = EXCLUDED.body_en, body_ru = EXCLUDED.body_ru,
  requires_permission = EXCLUDED.requires_permission,
  archived_at = NULL, updated_at = now(), updated_by = EXCLUDED.updated_by
RETURNING *;

-- name: ArchiveCannedResponse :one
UPDATE support_canned_responses SET archived_at = now(), updated_at = now()
WHERE id = sqlc.arg(response_id) AND archived_at IS NULL
RETURNING *;

-- name: CountCannedResponseUse :exec
UPDATE support_canned_responses SET usage_count = usage_count + 1
WHERE id = sqlc.arg(response_id);

-- ---------------------------------------------------------------------------
-- Workload and response-time reporting
-- ---------------------------------------------------------------------------

-- name: SupportWorkloadReport :many
-- Per-operator workload over a window.
--
-- The definitions are deliberately narrow and are documented beside the report:
-- `replies` counts operator messages the operator actually wrote, `resolved`
-- counts tickets that reached a terminal state while assigned to them, and the
-- response measure is the median rather than the mean, because one ticket
-- answered a week late would otherwise make a good week look bad.
SELECT
  a.id AS operator_id,
  a.display_name,
  (SELECT count(*)::bigint FROM support_messages m
    WHERE m.author_id = a.id AND m.created_at >= sqlc.arg(since)) AS replies,
  (SELECT count(*)::bigint FROM support_tickets t
    WHERE t.assignee_id = a.id AND t.status IN ('open', 'pending')) AS open_tickets,
  (SELECT count(*)::bigint FROM support_tickets t
    WHERE t.assignee_id = a.id AND t.resolved_at >= sqlc.arg(since)) AS resolved_tickets,
  COALESCE((
    SELECT percentile_cont(0.5) WITHIN GROUP (
      ORDER BY extract(epoch FROM (t.first_response_at - t.created_at))
    )
    FROM support_tickets t
    WHERE t.assignee_id = a.id
      AND t.first_response_at IS NOT NULL
      AND t.first_response_at >= sqlc.arg(since)
  ), 0)::bigint AS median_first_response_seconds
FROM admin_users a
WHERE a.status = 'active'
ORDER BY a.display_name;

-- name: SupportDeskSummary :one
-- The desk at a glance: what is waiting, what is overdue, and how quickly the
-- desk is answering.
SELECT
  (SELECT count(*)::bigint FROM support_tickets WHERE status IN ('open', 'pending')) AS open_tickets,
  (SELECT count(*)::bigint FROM support_tickets
    WHERE status IN ('open', 'pending') AND assignee_id IS NULL) AS unassigned_tickets,
  (SELECT count(*)::bigint FROM support_tickets t
    JOIN support_queues q ON q.id = t.queue_id
    WHERE t.status IN ('open', 'pending')
      AND q.first_response_target_seconds > 0
      AND t.first_response_at IS NULL
      AND t.created_at < now() - make_interval(secs => q.first_response_target_seconds))
    AS breached_tickets,
  (SELECT count(*)::bigint FROM support_tickets t
    WHERE t.resolved_at >= sqlc.arg(since)) AS resolved_in_window,
  COALESCE((
    SELECT percentile_cont(0.5) WITHIN GROUP (
      ORDER BY extract(epoch FROM (t.first_response_at - t.created_at))
    )
    FROM support_tickets t
    WHERE t.first_response_at IS NOT NULL AND t.first_response_at >= sqlc.arg(since)
  ), 0)::bigint AS median_first_response_seconds;
