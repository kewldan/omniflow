-- The reminder constructor: operator-built repeating reminders by segment.

-- name: ListReminders :many
SELECT * FROM reminders ORDER BY created_at;

-- name: ListActiveReminders :many
SELECT * FROM reminders WHERE active ORDER BY created_at;

-- name: UpsertReminder :one
INSERT INTO reminders (
  id, name, active, category, segment, segment_days, balance_below_minor, currency,
  repeat_every_days, max_sends, title_ru, body_ru, title_en, body_en, button_route,
  show_in_cabinet, updated_by
) VALUES (
  COALESCE(sqlc.narg(id)::uuid, gen_random_uuid()), sqlc.arg(name), sqlc.arg(active), sqlc.arg(category),
  sqlc.arg(segment), sqlc.arg(segment_days), sqlc.arg(balance_below_minor), sqlc.arg(currency),
  sqlc.arg(repeat_every_days), sqlc.arg(max_sends), sqlc.arg(title_ru), sqlc.arg(body_ru),
  sqlc.arg(title_en), sqlc.arg(body_en), sqlc.narg(button_route), sqlc.arg(show_in_cabinet),
  sqlc.narg(updated_by)
)
ON CONFLICT (id) DO UPDATE SET
  name = EXCLUDED.name, active = EXCLUDED.active, category = EXCLUDED.category,
  segment = EXCLUDED.segment, segment_days = EXCLUDED.segment_days,
  balance_below_minor = EXCLUDED.balance_below_minor, currency = EXCLUDED.currency,
  repeat_every_days = EXCLUDED.repeat_every_days, max_sends = EXCLUDED.max_sends,
  title_ru = EXCLUDED.title_ru, body_ru = EXCLUDED.body_ru, title_en = EXCLUDED.title_en,
  body_en = EXCLUDED.body_en, button_route = EXCLUDED.button_route,
  show_in_cabinet = EXCLUDED.show_in_cabinet, updated_by = EXCLUDED.updated_by, updated_at = now()
RETURNING *;

-- name: DeleteReminder :execrows
DELETE FROM reminders WHERE id = sqlc.arg(id);

-- name: ListReminderAudience :many
-- Customers inside a reminder's segment who are due for it: not dismissed,
-- under its send count, past its repeat interval, and not sent any reminder
-- in the last day — one reminder a day per person, whatever the operator has
-- configured.
SELECT u.id AS user_id
FROM users u
JOIN reminders r ON r.id = sqlc.arg(reminder_id)
LEFT JOIN reminder_states st ON st.reminder_id = r.id AND st.user_id = u.id
WHERE u.status = 'active'
  AND u.id > sqlc.arg(after_user)::uuid
  AND st.dismissed_at IS NULL
  AND COALESCE(st.sends, 0) < r.max_sends
  AND (st.last_sent_at IS NULL OR st.last_sent_at <= now() - make_interval(days => r.repeat_every_days))
  AND NOT EXISTS (
    SELECT 1 FROM reminder_states recent
    WHERE recent.user_id = u.id AND recent.last_sent_at > now() - interval '1 day'
  )
  AND reminder_segment_holds(r.id, u.id)
ORDER BY u.id
LIMIT sqlc.arg(batch_size);

-- name: CountReminderAudience :one
SELECT count(*)::bigint FROM users u
WHERE u.status = 'active' AND reminder_segment_holds(sqlc.arg(reminder_id), u.id);

-- name: RecordReminderSend :exec
INSERT INTO reminder_states (reminder_id, user_id, sends, last_sent_at)
VALUES (sqlc.arg(reminder_id), sqlc.arg(user_id), 1, now())
ON CONFLICT (reminder_id, user_id) DO UPDATE SET sends = reminder_states.sends + 1, last_sent_at = now();

-- name: ListCabinetReminders :many
-- The cabinet cards one customer sees: active reminders shown in the cabinet
-- whose segment holds the customer now and which they have not dismissed.
SELECT r.id, r.title_ru, r.body_ru, r.title_en, r.body_en, r.button_route
FROM reminders r
WHERE r.active AND r.show_in_cabinet
  AND reminder_segment_holds(r.id, sqlc.arg(user_id))
  AND NOT EXISTS (
    SELECT 1 FROM reminder_states st
    WHERE st.reminder_id = r.id AND st.user_id = sqlc.arg(user_id) AND st.dismissed_at IS NOT NULL
  )
ORDER BY r.created_at;

-- name: DismissReminder :exec
INSERT INTO reminder_states (reminder_id, user_id, dismissed_at)
SELECT r.id, sqlc.arg(user_id), now() FROM reminders r WHERE r.id = sqlc.arg(reminder_id)
ON CONFLICT (reminder_id, user_id) DO UPDATE SET dismissed_at = now();
