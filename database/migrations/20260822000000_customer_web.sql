-- Omniflow v0.9 customer web foundation: sign-in, sessions, and account security.
--
-- The customer product has run entirely through Telegram until now, where the
-- transport itself proves who is speaking. A browser proves nothing, so this
-- migration adds the three things a web session needs that the bot never did:
-- a way to establish identity, a session to carry it, and a record the customer
-- themselves can read.
--
-- What it deliberately does not add is a second identity model. `users` remains
-- the canonical customer and `identities` remains the set of ways to reach one,
-- so a customer who signs in with Telegram in the browser lands on exactly the
-- account the bot has been using. OIDC subjects join `identities` under a
-- provider of `oidc:<slug>` rather than getting a table of their own; that keeps
-- one unlink rule (`customer.CanUnlink`), one conflict rule, and one place the
-- import and anonymization paths have to know about.
--
-- Session, magic-link, and security-event rows are append-only apart from the
-- lifecycle columns each table documents below.

-- ---------------------------------------------------------------------------
-- Sessions
-- ---------------------------------------------------------------------------

-- Only the SHA-256 of the session token is stored, so a database read never
-- yields a usable cookie and an offline backup cannot be replayed against a
-- live installation. This mirrors `admin_sessions` on purpose: the two surfaces
-- have different lifetimes and different threat models, but the storage
-- discipline that makes a stolen dump useless is the same one.
--
-- Two expiries run at once. `idle_expires_at` slides forward while the customer
-- keeps using the panel; `absolute_expires_at` never moves, so a session kept
-- warm by a background tab still ends at a fixed horizon.
--
-- `auth_method` records how the session was actually established. It is not
-- decoration: revoking every session created through a provider an operator has
-- just disabled needs to know which those were, and a customer reading their own
-- security list is entitled to see "signed in with Telegram" rather than a bare
-- timestamp.
--
-- Documented mutable lifecycle: `last_seen_at`, `idle_expires_at`, `token_hash`
-- with `rotated_at` (rotation), and the revocation columns. Everything else is
-- written once.
CREATE TABLE customer_sessions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),

  -- Per-session CSRF secret. The double-submit token handed to the browser is
  -- derived from this value, so a token minted for one session cannot be
  -- replayed inside another.
  csrf_secret bytea NOT NULL CHECK (octet_length(csrf_secret) = 32),

  auth_method text NOT NULL CHECK (auth_method IN ('telegram', 'magic_link', 'oidc')),
  -- The OIDC provider slug when `auth_method` is 'oidc', null otherwise. It is
  -- a plain text copy rather than a foreign key: deleting a provider must not
  -- cascade away the sessions it created, because those sessions are exactly
  -- what an operator needs to revoke after removing it.
  auth_provider text CHECK (auth_provider IS NULL OR char_length(auth_provider) BETWEEN 1 AND 40),

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
    OR revoked_reason IN (
      'logout', 'logout_all', 'session_revoked', 'identity_unlinked',
      'provider_disabled', 'account_suspended', 'account_deleted', 'expired'
    )
  ),

  CONSTRAINT customer_sessions_absolute_after_creation CHECK (absolute_expires_at >= created_at),
  CONSTRAINT customer_sessions_revocation_complete
    CHECK ((revoked_at IS NULL) = (revoked_reason IS NULL)),
  CONSTRAINT customer_sessions_provider_present_for_oidc
    CHECK ((auth_method = 'oidc') = (auth_provider IS NOT NULL))
);

-- The customer's own "active sessions" list reads live rows newest-first; the
-- expiry index serves the retention sweep that removes rows long past use.
CREATE INDEX customer_sessions_live_idx
  ON customer_sessions (user_id, last_seen_at DESC) WHERE revoked_at IS NULL;
CREATE INDEX customer_sessions_expiry_idx
  ON customer_sessions (absolute_expires_at) WHERE revoked_at IS NULL;

-- ---------------------------------------------------------------------------
-- Magic links
-- ---------------------------------------------------------------------------

-- The operator-enabled fallback when the Telegram login widget is unavailable —
-- typically because the installation's domain has not been bound in BotFather.
--
-- There is no email transport in this repository, so delivery is the bot: the
-- link is sent to the customer's own Telegram chat. That makes it a genuine
-- second route rather than a duplicate of the first, because it needs no widget
-- and no domain binding, and it cannot be used to reach an account whose owner
-- does not already control the linked Telegram identity.
--
-- Only the digest is stored, the row is consumed by setting `consumed_at`, and
-- consumption is a conditional UPDATE rather than a read-then-write, so two
-- browsers racing on one link produce exactly one session. Consumed rows are
-- retained so the customer's security list can show that the route was taken.
CREATE TABLE customer_magic_links (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
  requested_ip inet,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz,
  consumed_ip inet,
  CONSTRAINT customer_magic_links_expiry_after_creation CHECK (expires_at > created_at),
  CONSTRAINT customer_magic_links_consumption_complete
    CHECK (consumed_ip IS NULL OR consumed_at IS NOT NULL)
);

CREATE INDEX customer_magic_links_pending_idx
  ON customer_magic_links (user_id, created_at DESC) WHERE consumed_at IS NULL;

-- ---------------------------------------------------------------------------
-- Customer-visible security events
-- ---------------------------------------------------------------------------

-- Separate from `audit_events` because the audience is different, and that
-- difference is the whole point. `audit_events` is an operator record, searched
-- and exported under operator permissions, and deciding per row which of its
-- fields a customer may see would put a disclosure decision in the read path.
-- This table is written to be shown: the `event` vocabulary is closed, and there
-- is no column for free text, an amount, a link, or another party's identifier.
--
-- Append-only. Nothing updates or deletes a row here; the retention sweep is the
-- only writer that removes one, and only after the documented window.
CREATE TABLE customer_security_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  event text NOT NULL CHECK (event IN (
    'signed_in', 'signed_out', 'signed_out_all',
    'session_revoked', 'magic_link_requested',
    'identity_linked', 'identity_unlinked',
    'subscription_key_rotated', 'device_removed', 'devices_removed_all'
  )),

  -- How the actor reached the installation. Recorded for the customer's benefit:
  -- an unfamiliar address is the signal a security list exists to surface.
  ip inet,
  user_agent text CHECK (user_agent IS NULL OR char_length(user_agent) <= 400),
  request_id text CHECK (request_id IS NULL OR char_length(request_id) <= 64),

  -- Bounded, non-identifying context — an auth method, a device label the
  -- customer chose, a provider slug. The API validates the shape on write.
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,

  occurred_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX customer_security_events_recent_idx
  ON customer_security_events (user_id, occurred_at DESC);

-- ---------------------------------------------------------------------------
-- Customer OIDC providers
-- ---------------------------------------------------------------------------

-- Configuration, not code. An operator supplies a discovery document and client
-- credentials; nothing in the application branches on which provider it is. The
-- shipped presets for Google, Yandex, and Discord are prefilled values in the
-- panel — they produce an ordinary row here and take no special path.
--
-- Several providers may be enabled at once, which is why `sort_order` and the
-- presentation columns exist: the sign-in screen renders whatever is enabled, in
-- the order and under the labels the operator chose.
--
-- OIDC never becomes mandatory. An installation with no row here signs customers
-- in with Telegram exactly as before, which is the default posture.
CREATE TABLE customer_oidc_providers (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$'),
  display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 80),
  issuer text NOT NULL CHECK (issuer ~ '^https://'),
  discovery_url text NOT NULL CHECK (discovery_url ~ '^https://'),
  client_id text NOT NULL CHECK (char_length(client_id) BETWEEN 1 AND 256),
  client_secret_ciphertext bytea,
  scopes text[] NOT NULL DEFAULT '{openid,email,profile}',
  enabled boolean NOT NULL DEFAULT false,

  -- Presentation the operator controls. `icon` names one of the sign-in icons
  -- the panel offers rather than carrying a URL, so enabling a provider can
  -- never make the sign-in page fetch from a third-party host.
  icon text CHECK (icon IS NULL OR icon ~ '^[a-z0-9][a-z0-9-]{0,38}$'),
  sort_order integer NOT NULL DEFAULT 0,

  -- A provider may assert an address it has not verified. Requiring the verified
  -- claim by default stops a provider from minting access to an account it does
  -- not actually control.
  require_verified_email boolean NOT NULL DEFAULT true,

  -- Whether a subject that matches no existing customer may create one. Unlike
  -- the operator panel, this defaults to true: a customer panel that refuses
  -- everyone who has not already used the bot is not a sign-in method, it is a
  -- second lock. Auto-provisioning here creates an ordinary customer with no
  -- entitlement and no balance, which is what a visitor arriving to buy needs.
  allow_auto_provision boolean NOT NULL DEFAULT true,

  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  disabled_at timestamptz
);

CREATE INDEX customer_oidc_providers_enabled_idx
  ON customer_oidc_providers (sort_order, slug) WHERE enabled;

-- ---------------------------------------------------------------------------
-- Installation settings
-- ---------------------------------------------------------------------------

-- Customer sign-in is an installation setting like every other section: which
-- routes are offered, how long a session lives, whether the magic-link fallback
-- is available at all.
--
-- The existing constraint is the one PostgreSQL derived from the inline column
-- check, so it is dropped by that derived name and replaced by an explicit one.
ALTER TABLE installation_settings
  DROP CONSTRAINT installation_settings_section_check;

ALTER TABLE installation_settings
  ADD CONSTRAINT installation_settings_section_known CHECK (section IN (
    'branding', 'remnawave', 'telegram', 'operator_group', 'required_channels',
    'maintenance', 'notifications', 'telemetry', 'backup', 'security', 'ai', 'mcp',
    'customer_auth'
  ));
