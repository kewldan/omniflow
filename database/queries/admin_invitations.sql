-- ---------------------------------------------------------------------------
-- Operator invitations
-- ---------------------------------------------------------------------------

-- name: RetireOpenAdminInvitations :execrows
-- Retires every open invitation for an address before a new one is issued, so
-- only the newest link can ever be redeemed.
UPDATE admin_invitations
SET revoked_at = now(), revoked_by = sqlc.arg(revoked_by)
WHERE email_normalized = sqlc.arg(email_normalized)
  AND accepted_at IS NULL AND revoked_at IS NULL;

-- name: CreateAdminInvitation :one
INSERT INTO admin_invitations (
  email, email_normalized, roles, token_hash, invited_by, expires_at
) VALUES (
  sqlc.arg(email), sqlc.arg(email_normalized), sqlc.arg(roles)::text[],
  sqlc.arg(token_hash), sqlc.arg(invited_by), now() + sqlc.arg(lifetime)::interval
)
RETURNING *;

-- name: ListOpenAdminInvitations :many
-- Open means neither accepted nor revoked. Expired rows are included and
-- labelled by the panel, because an invitation that lapsed unseen is exactly
-- the one the inviter needs to notice and reissue.
SELECT i.id, i.email, i.roles, i.created_at, i.expires_at,
       i.invited_by, u.display_name AS invited_by_name
FROM admin_invitations i
JOIN admin_users u ON u.id = i.invited_by
WHERE i.accepted_at IS NULL AND i.revoked_at IS NULL
ORDER BY i.created_at DESC
LIMIT sqlc.arg(page_size);

-- name: GetAdminInvitationByToken :one
SELECT * FROM admin_invitations WHERE token_hash = sqlc.arg(token_hash);

-- name: LockAdminInvitationByToken :one
-- Taken inside the acceptance transaction, so two submissions of the same link
-- serialise here and the second sees the first one's acceptance.
SELECT * FROM admin_invitations WHERE token_hash = sqlc.arg(token_hash) FOR UPDATE;

-- name: AcceptAdminInvitation :one
UPDATE admin_invitations
SET accepted_at = now(), accepted_admin_user_id = sqlc.arg(admin_user_id)
WHERE id = sqlc.arg(invitation_id)
  AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()
RETURNING *;

-- name: RevokeAdminInvitation :one
UPDATE admin_invitations
SET revoked_at = now(), revoked_by = sqlc.arg(revoked_by)
WHERE id = sqlc.arg(invitation_id) AND accepted_at IS NULL AND revoked_at IS NULL
RETURNING *;

-- name: LockAdminRoleChanges :exec
-- Serialises every change that could remove an owner. Two owners demoting each
-- other at the same moment would otherwise each count the other as the
-- remaining owner and both succeed, leaving none.
SELECT pg_advisory_xact_lock(hashtext('omniflow.admin_roles'));
