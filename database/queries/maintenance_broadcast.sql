-- name: QueueMaintenanceBroadcast :execrows
-- Records a customer notice for a maintenance transition, when the maintenance
-- settings ask for one. Only a real transition counts: an event whose action
-- repeats the previous event's — an operator rewording the notice while
-- maintenance stays on — announces nothing new. The notice and the expected
-- return are copied from the current state.
INSERT INTO maintenance_broadcasts (event_id, transition, notice_ru, notice_en, expected_return_at, email)
SELECT e.id, e.action, COALESCE(m.notice_ru, ''), COALESCE(m.notice_en, ''), m.expected_return_at,
       COALESCE((s.document->>'broadcastEmail')::boolean, false)
FROM maintenance_events e
JOIN installation_settings s ON s.section = 'maintenance'
LEFT JOIN maintenance_state m ON true
WHERE e.id = sqlc.arg(event_id)::uuid
  AND COALESCE((s.document->>'broadcastCustomers')::boolean, false)
  AND e.action IS DISTINCT FROM (
    SELECT previous.action FROM maintenance_events previous
    WHERE previous.id <> e.id AND previous.occurred_at <= e.occurred_at
    ORDER BY previous.occurred_at DESC LIMIT 1)
ON CONFLICT (event_id) DO NOTHING;
