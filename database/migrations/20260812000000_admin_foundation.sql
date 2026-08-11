-- Omniflow v0.6 admin panel foundation and access control.
--
-- Adds the operator identity domain: administrator accounts, role grants,
-- authenticated sessions, TOTP recovery codes, password resets, the one-time
-- first-owner bootstrap token, and optional OIDC provider configuration. It
-- also widens the existing append-only audit trail so operator actions can be
-- searched and exported by category and outcome.
--
-- Operator accounts are entirely separate from `users`, which remains the
-- customer identity table. An administrator is never a VPN customer by virtue
-- of holding a panel account, and nothing here creates a Remnawave entitlement.
--
-- Session, reset, recovery-code, bootstrap, and audit rows are append-only
-- apart from the documented lifecycle columns each table declares below.

-- ---------------------------------------------------------------------------
-- Administrator accounts
-- ---------------------------------------------------------------------------

-- `email_normalized` is the lookup key and carries the unique constraint, so
-- "Owner@example.com" and "owner@example.com" cannot become two accounts. The
-- original casing is preserved in `email` purely for display.
--
-- `password_hash` holds a full PHC-encoded argon2id string, which embeds the
-- algorithm parameters. Re-tuning the cost therefore does not need a migration:
-- an existing hash keeps verifying under its own recorded parameters and is
-- upgraded in place on the owner's next successful sign-in.
--
-- The column is nullable because an OIDC-only operator may never hold a
-- password. That an account retains at least one usable credential cannot be a
-- table constraint, since the alternative credential lives in
-- `admin_oidc_identities`; the invariant is enforced in `internal/adminauthpg`
-- inside the same transaction as any credential removal.
CREATE TABLE admin_users (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email text NOT NULL CHECK (char_length(email) BETWEEN 3 AND 254),
  email_normalized text NOT NULL UNIQUE CHECK (char_length(email_normalized) BETWEEN 3 AND 254),
  display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 80),
  password_hash text CHECK (password_hash IS NULL OR char_length(password_hash) BETWEEN 16 AND 512),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'disabled')),
  locale text NOT NULL DEFAULT 'en' CHECK (locale IN ('en', 'ru')),
  timezone text NOT NULL DEFAULT 'UTC',

  -- TOTP secrets are sealed with APP_DATA_ENCRYPTION_KEY exactly like customer
  -- contact values, so a database dump alone never yields a working second
  -- factor. `totp_confirmed_at` stays null while enrolment is in progress, so a
  -- half-finished enrolment can never satisfy a login challenge.
  totp_secret_ciphertext bytea,
  totp_confirmed_at timestamptz,

  -- Lockout state. `failed_login_count` drives the backoff curve and resets to
  -- zero on any successful authentication.
  failed_login_count integer NOT NULL DEFAULT 0 CHECK (failed_login_count >= 0),
  locked_until timestamptz,

  password_changed_at timestamptz,
  last_login_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  disabled_at timestamptz,

  -- An account must be reachable by at least one credential. OIDC-only accounts
  -- satisfy this through a linked subject rather than a password.
  CONSTRAINT admin_users_totp_enrolment_complete
    CHECK (totp_confirmed_at IS NULL OR totp_secret_ciphertext IS NOT NULL)
);

CREATE INDEX admin_users_active_idx ON admin_users (created_at DESC) WHERE status = 'active';

-- ---------------------------------------------------------------------------
-- Role grants
-- ---------------------------------------------------------------------------

-- Roles are a fixed, built-in set in v0.6. The permission catalogue and the
-- role-to-permission mapping live in Go (`internal/rbac`) so authorization is
-- unit-testable and cannot drift from the enforced rules; this table records
-- only which roles an operator holds. Operator-defined custom roles are v0.7
-- work and will extend, not replace, this table.
CREATE TABLE admin_user_roles (
  admin_user_id uuid NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
  role text NOT NULL CHECK (
    role IN ('owner', 'administrator', 'support', 'finance', 'marketing', 'auditor')
  ),
  granted_at timestamptz NOT NULL DEFAULT now(),
  granted_by uuid REFERENCES admin_users(id),
  PRIMARY KEY (admin_user_id, role)
);

-- The installation must never lose its last owner. This partial index does not
-- enforce that on its own, but it makes the "how many owners remain" guard in
-- `internal/adminauthpg` an index-only lookup on every role change.
CREATE INDEX admin_user_roles_owners_idx ON admin_user_roles (admin_user_id) WHERE role = 'owner';

-- ---------------------------------------------------------------------------
-- Sessions
-- ---------------------------------------------------------------------------

-- Only the SHA-256 of the session token is stored. A database read therefore
-- never yields a usable cookie, and a leaked backup cannot be replayed against
-- a live installation.
--
-- Two expiries run at once: `idle_expires_at` slides forward while the operator
-- keeps working, and `absolute_expires_at` never moves, so a session that is
-- kept warm indefinitely still ends at a fixed horizon.
--
-- `pending_totp` marks a session that has passed the password factor but not
-- yet the second one. Such a session authenticates nothing beyond the TOTP
-- challenge endpoints, which is enforced in the API middleware.
--
-- Documented mutable lifecycle: `last_seen_at`, `idle_expires_at`, `token_hash`
-- and `rotated_at` (rotation), `pending_totp` (challenge completion), and the
-- revocation columns. Everything else is written once.
CREATE TABLE admin_sessions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  admin_user_id uuid NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
  token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),

  -- Per-session CSRF secret. The double-submit token handed to the browser is
  -- derived from this value, so a token minted for one session cannot be
  -- replayed inside another.
  csrf_secret bytea NOT NULL CHECK (octet_length(csrf_secret) = 32),

  pending_totp boolean NOT NULL DEFAULT false,
  -- Authentication methods this session actually completed, e.g. {password,totp}.
  auth_methods text[] NOT NULL DEFAULT '{}',

  ip inet,
  user_agent text CHECK (user_agent IS NULL OR char_length(user_agent) <= 400),

  created_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  rotated_at timestamptz NOT NULL DEFAULT now(),
  idle_expires_at timestamptz NOT NULL,
  absolute_expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  revoked_reason text CHECK (
    revoked_reason IS NULL
    OR revoked_reason IN ('logout', 'logout_all', 'password_change', 'admin_revoked', 'expired')
  ),

  CONSTRAINT admin_sessions_absolute_after_idle CHECK (absolute_expires_at >= created_at),
  CONSTRAINT admin_sessions_revocation_complete
    CHECK ((revoked_at IS NULL) = (revoked_reason IS NULL))
);

CREATE INDEX admin_sessions_live_idx
  ON admin_sessions (admin_user_id, last_seen_at DESC) WHERE revoked_at IS NULL;
CREATE INDEX admin_sessions_expiry_idx ON admin_sessions (absolute_expires_at) WHERE revoked_at IS NULL;

-- ---------------------------------------------------------------------------
-- Recovery codes
-- ---------------------------------------------------------------------------

-- Single-use fallbacks for a lost authenticator. Codes are stored only as
-- SHA-256 digests and are consumed by setting `used_at`; a used row is retained
-- so the audit trail can show that a recovery path was taken.
CREATE TABLE admin_recovery_codes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  admin_user_id uuid NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
  code_hash bytea NOT NULL CHECK (octet_length(code_hash) = 32),
  created_at timestamptz NOT NULL DEFAULT now(),
  used_at timestamptz,
  UNIQUE (admin_user_id, code_hash)
);

CREATE INDEX admin_recovery_codes_unused_idx
  ON admin_recovery_codes (admin_user_id) WHERE used_at IS NULL;

-- ---------------------------------------------------------------------------
-- Password resets
-- ---------------------------------------------------------------------------

-- A reset row is created for every request that names a real account, and the
-- endpoint answers identically whether or not one was created, so the response
-- never discloses whether an address is registered.
CREATE TABLE admin_password_resets (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  admin_user_id uuid NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
  token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
  requested_ip inet,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  CONSTRAINT admin_password_resets_expiry_after_creation CHECK (expires_at > created_at)
);

CREATE INDEX admin_password_resets_pending_idx
  ON admin_password_resets (admin_user_id, created_at DESC) WHERE used_at IS NULL;

-- ---------------------------------------------------------------------------
-- First-owner bootstrap
-- ---------------------------------------------------------------------------

-- A fresh installation has no operator account and therefore no way to sign in.
-- The API mints exactly one setup token, prints it to the server log once, and
-- accepts it a single time to create the first owner. `consumed_at` closes the
-- window permanently; the API additionally refuses bootstrap once any
-- administrator exists, so a leaked-but-unused token cannot be redeemed later.
CREATE TABLE admin_setup_tokens (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz,
  consumed_by uuid REFERENCES admin_users(id),
  CONSTRAINT admin_setup_tokens_expiry_after_creation CHECK (expires_at > created_at),
  CONSTRAINT admin_setup_tokens_consumption_complete
    CHECK ((consumed_at IS NULL) = (consumed_by IS NULL))
);

-- ---------------------------------------------------------------------------
-- Optional OIDC configuration
-- ---------------------------------------------------------------------------

-- OIDC is configuration, not code: an operator supplies a discovery document
-- and client credentials, and no provider-specific branch exists anywhere in
-- the application. An installation with no row here authenticates with
-- passwords alone, which is the default and fully supported posture.
CREATE TABLE admin_oidc_providers (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$'),
  display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 80),
  issuer text NOT NULL CHECK (issuer ~ '^https://'),
  discovery_url text NOT NULL CHECK (discovery_url ~ '^https://'),
  client_id text NOT NULL CHECK (char_length(client_id) BETWEEN 1 AND 256),
  client_secret_ciphertext bytea,
  scopes text[] NOT NULL DEFAULT '{openid,email,profile}',
  enabled boolean NOT NULL DEFAULT false,

  -- An external provider may assert an address it has not verified. Requiring
  -- the verified claim by default keeps a provider from being able to mint
  -- access to an operator account it does not actually control.
  require_verified_email boolean NOT NULL DEFAULT true,

  -- Auto-provisioning is off by default: a subject that matches no existing
  -- operator is rejected rather than silently granted a panel account.
  allow_auto_provision boolean NOT NULL DEFAULT false,
  auto_provision_role text CHECK (
    auto_provision_role IS NULL
    OR auto_provision_role IN ('administrator', 'support', 'finance', 'marketing', 'auditor')
  ),

  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT admin_oidc_providers_auto_provision_role_present
    CHECK (allow_auto_provision = false OR auto_provision_role IS NOT NULL)
);

-- Links an operator account to an external subject. The pair is unique per
-- provider, so two operators can never claim one external identity.
CREATE TABLE admin_oidc_identities (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  admin_user_id uuid NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
  provider_id uuid NOT NULL REFERENCES admin_oidc_providers(id) ON DELETE CASCADE,
  subject text NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 256),
  linked_at timestamptz NOT NULL DEFAULT now(),
  last_login_at timestamptz,
  UNIQUE (provider_id, subject),
  UNIQUE (admin_user_id, provider_id)
);

-- ---------------------------------------------------------------------------
-- Audit trail
-- ---------------------------------------------------------------------------

-- `audit_events` already carries actor, target, action, reason, request ID, and
-- metadata. v0.6 adds the two axes the panel filters and exports on, and the
-- indexes those filters need. Both columns are defaulted so every row written
-- by v0.5 code remains valid and classified.
ALTER TABLE audit_events
  ADD COLUMN category text NOT NULL DEFAULT 'system'
    CONSTRAINT audit_events_category_known CHECK (
      category IN (
        'authentication', 'authorization', 'configuration',
        'customer', 'financial', 'support', 'marketing', 'system'
      )
    ),
  ADD COLUMN outcome text NOT NULL DEFAULT 'success'
    CONSTRAINT audit_events_outcome_known CHECK (outcome IN ('success', 'failure', 'denied'));

-- The existing actor vocabulary has no term for a panel operator: 'operator'
-- was minted for the Telegram operator surface. Widening the check keeps both
-- meanings addressable rather than overloading one value.
ALTER TABLE audit_events
  DROP CONSTRAINT audit_events_actor_type_check;

ALTER TABLE audit_events
  ADD CONSTRAINT audit_events_actor_type_known CHECK (
    actor_type IN ('customer', 'operator', 'admin', 'system', 'provider')
  );

-- The audit browser lists newest-first and filters by category, actor, and
-- action. A plain descending index on `occurred_at` serves the unfiltered list;
-- the composite indexes serve the two filters the panel exposes by default.
CREATE INDEX audit_events_recent_idx ON audit_events (occurred_at DESC);
CREATE INDEX audit_events_category_idx ON audit_events (category, occurred_at DESC);
CREATE INDEX audit_events_actor_idx ON audit_events (actor_type, actor_id, occurred_at DESC);
CREATE INDEX audit_events_action_idx ON audit_events (action, occurred_at DESC);
