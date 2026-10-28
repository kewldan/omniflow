-- Operator invitations.
--
-- Until now the only ways to add an operator were the first-owner bootstrap and
-- an API call that required the owner to choose the newcomer's password. An
-- invitation inverts that: the owner names an address and a role set, and the
-- person who will hold the account chooses their own name and password by
-- redeeming a link.
--
-- The link is a bearer credential for creating an account with the listed
-- roles, so it is handled exactly like a password reset token: only its
-- SHA-256 is stored, it expires, and redemption is a conditional UPDATE so two
-- browsers racing on one link create one account. There is no email transport;
-- the inviter is shown the link once and passes it on themselves.
--
-- Rows are never deleted. An invitation ends in exactly one of three ways —
-- accepted, revoked, or expired — and the first two are recorded here. The
-- audit trail carries who did what; this table carries the state.
CREATE TABLE admin_invitations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email text NOT NULL CHECK (char_length(email) BETWEEN 3 AND 254),
  email_normalized text NOT NULL CHECK (char_length(email_normalized) BETWEEN 3 AND 254),
  roles text[] NOT NULL,
  token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
  invited_by uuid NOT NULL REFERENCES admin_users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  accepted_at timestamptz,
  accepted_admin_user_id uuid REFERENCES admin_users(id),
  revoked_at timestamptz,
  revoked_by uuid REFERENCES admin_users(id),
  CONSTRAINT admin_invitations_roles_known CHECK (
    cardinality(roles) BETWEEN 1 AND 6
    AND roles <@ ARRAY['owner', 'administrator', 'support', 'finance', 'marketing', 'auditor']::text[]
  ),
  CONSTRAINT admin_invitations_expiry_after_creation CHECK (expires_at > created_at),
  CONSTRAINT admin_invitations_acceptance_complete
    CHECK ((accepted_at IS NULL) = (accepted_admin_user_id IS NULL)),
  CONSTRAINT admin_invitations_revocation_complete
    CHECK ((revoked_at IS NULL) = (revoked_by IS NULL)),
  CONSTRAINT admin_invitations_settled_once
    CHECK (accepted_at IS NULL OR revoked_at IS NULL)
);

-- One live invitation per address. Inviting the same person again retires the
-- earlier link first, so an intercepted older link stops working the moment a
-- new one is issued.
CREATE UNIQUE INDEX admin_invitations_one_open_per_email_idx
  ON admin_invitations (email_normalized)
  WHERE accepted_at IS NULL AND revoked_at IS NULL;

-- The pending list on the operators screen.
CREATE INDEX admin_invitations_open_idx
  ON admin_invitations (created_at DESC)
  WHERE accepted_at IS NULL AND revoked_at IS NULL;
