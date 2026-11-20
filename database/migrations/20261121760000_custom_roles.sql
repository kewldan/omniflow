-- Operator-defined roles on top of the six built-in ones. A custom role is a
-- name for a set of permissions from the compiled-in catalogue; internal/rbac
-- validates every definition before it can be granted, and refuses one that
-- carries admins.write or admins.roles.
CREATE TABLE admin_custom_roles (
  role text NOT NULL,
  name text NOT NULL,
  permissions text[] NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid,
  CONSTRAINT admin_custom_roles_pkey PRIMARY KEY (role),
  CONSTRAINT admin_custom_roles_key_shape CHECK (role ~ '^custom-[a-z0-9-]{2,32}$'),
  CONSTRAINT admin_custom_roles_name_length CHECK (char_length(name) BETWEEN 1 AND 80),
  CONSTRAINT admin_custom_roles_permissions_present CHECK (cardinality(permissions) >= 1),
  CONSTRAINT admin_custom_roles_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES admin_users(id) ON DELETE SET NULL
);

ALTER TABLE admin_user_roles DROP CONSTRAINT admin_user_roles_role_check;
ALTER TABLE admin_user_roles ADD CONSTRAINT admin_user_roles_role_check CHECK (
  role = ANY (ARRAY['owner', 'administrator', 'support', 'finance', 'marketing', 'auditor']::text[])
  OR role ~ '^custom-[a-z0-9-]{2,32}$'
);

ALTER TABLE admin_invitations DROP CONSTRAINT admin_invitations_roles_known;
ALTER TABLE admin_invitations ADD CONSTRAINT admin_invitations_roles_known CHECK (
  cardinality(roles) BETWEEN 1 AND 6
  AND array_to_string(roles, ',') ~ '^((owner|administrator|support|finance|marketing|auditor|custom-[a-z0-9-]{2,32})(,|$))+$'
);
