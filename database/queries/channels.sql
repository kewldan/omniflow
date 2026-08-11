-- Mandatory channel subscription.

-- name: ListRequiredChannels :many
SELECT * FROM required_channels ORDER BY sort_order, title;

-- name: ListEnabledChannels :many
SELECT * FROM required_channels WHERE enabled ORDER BY sort_order, title;

-- name: UpsertRequiredChannel :one
INSERT INTO required_channels (
  telegram_chat_id, username, title, invite_url, enabled,
  require_for_purchase, require_for_activation, sort_order, created_by
) VALUES (
  sqlc.arg(telegram_chat_id), sqlc.narg(username), sqlc.arg(title), sqlc.narg(invite_url),
  sqlc.arg(enabled), sqlc.arg(require_for_purchase), sqlc.arg(require_for_activation),
  sqlc.arg(sort_order), sqlc.narg(created_by)
)
ON CONFLICT (telegram_chat_id) DO UPDATE SET
  username = EXCLUDED.username, title = EXCLUDED.title, invite_url = EXCLUDED.invite_url,
  enabled = EXCLUDED.enabled, require_for_purchase = EXCLUDED.require_for_purchase,
  require_for_activation = EXCLUDED.require_for_activation,
  sort_order = EXCLUDED.sort_order, updated_at = now()
RETURNING *;

-- name: DeleteRequiredChannel :exec
DELETE FROM required_channels WHERE id = $1;

-- name: ListCustomerMemberships :many
SELECT * FROM channel_memberships WHERE user_id = $1;

-- name: RecordMembership :one
-- `left_at` is set the first time absence is seen and cleared on return, so the
-- grace clock measures from when the customer actually left rather than from
-- when a sweep noticed.
INSERT INTO channel_memberships (user_id, channel_id, state, left_at)
VALUES (
  sqlc.arg(user_id), sqlc.arg(channel_id), sqlc.arg(state),
  CASE WHEN sqlc.arg(state)::text = 'absent' THEN now() ELSE NULL END
)
ON CONFLICT (user_id, channel_id) DO UPDATE SET
  state = EXCLUDED.state,
  checked_at = now(),
  left_at = CASE
    WHEN EXCLUDED.state = 'absent' THEN COALESCE(channel_memberships.left_at, now())
    WHEN EXCLUDED.state = 'member' THEN NULL
    ELSE channel_memberships.left_at
  END
RETURNING *;

-- name: ListCustomersForChannelRecheck :many
-- Customers whose membership answer has gone stale, oldest first.
--
-- A customer with no record at all is included, because "never checked" and
-- "checked long ago" need the same thing done about them.
SELECT DISTINCT r.user_id, r.telegram_id
FROM remnawave_users r
JOIN users u ON u.id = r.user_id AND u.status = 'active'
WHERE r.telegram_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM channel_exemptions x
    WHERE x.user_id = r.user_id AND (x.expires_at IS NULL OR x.expires_at > now()))
  AND (
    NOT EXISTS (SELECT 1 FROM channel_memberships m WHERE m.user_id = r.user_id)
    OR EXISTS (SELECT 1 FROM channel_memberships m
      WHERE m.user_id = r.user_id AND m.checked_at < sqlc.arg(stale_before))
  )
ORDER BY r.user_id
LIMIT sqlc.arg(page_size);

-- name: IsChannelExempt :one
SELECT EXISTS (
  SELECT 1 FROM channel_exemptions
  WHERE user_id = $1 AND (expires_at IS NULL OR expires_at > now())
) AS exempt;

-- name: ListChannelExemptions :many
SELECT x.*, COALESCE(a.display_name, '') AS granted_by_name
FROM channel_exemptions x
LEFT JOIN admin_users a ON a.id = x.granted_by
ORDER BY x.granted_at DESC
LIMIT sqlc.arg(page_size);

-- name: GrantChannelExemption :one
INSERT INTO channel_exemptions (user_id, reason, granted_by, expires_at)
VALUES (sqlc.arg(user_id), sqlc.arg(reason), sqlc.narg(granted_by), sqlc.narg(expires_at))
ON CONFLICT (user_id) DO UPDATE SET
  reason = EXCLUDED.reason, granted_by = EXCLUDED.granted_by,
  granted_at = now(), expires_at = EXCLUDED.expires_at
RETURNING *;

-- name: RevokeChannelExemption :exec
DELETE FROM channel_exemptions WHERE user_id = $1;

-- name: GetChannelEnforcement :one
SELECT * FROM channel_enforcement WHERE user_id = $1;

-- name: SetChannelEnforcement :one
INSERT INTO channel_enforcement (user_id, state, warned_at, grace_until, suspended_at, restored_at)
VALUES (
  sqlc.arg(user_id), sqlc.arg(state),
  CASE WHEN sqlc.arg(warn)::boolean THEN now() ELSE NULL END,
  sqlc.narg(grace_until),
  CASE WHEN sqlc.arg(state)::text = 'suspended' THEN now() ELSE NULL END,
  CASE WHEN sqlc.arg(restore)::boolean THEN now() ELSE NULL END
)
ON CONFLICT (user_id) DO UPDATE SET
  state = EXCLUDED.state,
  warned_at = CASE WHEN sqlc.arg(warn)::boolean THEN now() ELSE channel_enforcement.warned_at END,
  grace_until = EXCLUDED.grace_until,
  suspended_at = CASE
    WHEN EXCLUDED.state = 'suspended' THEN COALESCE(channel_enforcement.suspended_at, now())
    ELSE NULL END,
  restored_at = CASE WHEN sqlc.arg(restore)::boolean THEN now() ELSE channel_enforcement.restored_at END,
  updated_at = now()
RETURNING *;

-- name: ListChannelEnforcementDue :many
-- Warned customers whose grace has run out.
SELECT e.user_id, r.telegram_id
FROM channel_enforcement e
JOIN remnawave_users r ON r.user_id = e.user_id
WHERE e.state = 'warned' AND e.grace_until IS NOT NULL AND e.grace_until <= now()
ORDER BY e.grace_until
LIMIT sqlc.arg(page_size);

-- name: ChannelGateSettings :one
SELECT channel_grace_seconds, channel_recheck_seconds FROM commerce_settings WHERE singleton;
