-- Operator-defined roles.

-- name: ListCustomRoles :many
SELECT * FROM admin_custom_roles ORDER BY role;

-- name: UpsertCustomRole :one
INSERT INTO admin_custom_roles (role, name, permissions, updated_by)
VALUES (sqlc.arg(role), sqlc.arg(name), sqlc.arg(permissions), sqlc.narg(updated_by))
ON CONFLICT (role) DO UPDATE SET name = EXCLUDED.name, permissions = EXCLUDED.permissions,
  updated_by = EXCLUDED.updated_by, updated_at = now()
RETURNING *;

-- name: DeleteCustomRole :execrows
DELETE FROM admin_custom_roles WHERE role = sqlc.arg(role);

-- name: RevokeCustomRoleGrants :exec
-- A deleted role leaves nobody holding it, and no invitation offering it.
WITH revoked AS (DELETE FROM admin_user_roles WHERE role = sqlc.arg(role))
UPDATE admin_invitations SET roles = array_remove(roles, sqlc.arg(role)::text)
WHERE sqlc.arg(role)::text = ANY (roles) AND cardinality(roles) > 1;
