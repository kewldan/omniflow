-- Channel enforcement: what a membership lapse takes away and a rejoin gives
-- back.
--
-- Only entitlements still inside their paid period are listed. An expired or
-- superseded one has nothing to suspend, and a paused one is already off by an
-- operator's decision that a rejoin must not undo.

-- name: ListEntitlementsForChannelEnforcement :many
SELECT id, status
FROM entitlements
WHERE user_id = sqlc.arg(user_id)
  AND status IN ('pending', 'active', 'limited', 'disabled')
  AND ends_at > now()
ORDER BY created_at;
