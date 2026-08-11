-- Mandatory channel subscription.
--
-- An operator may require customers to be members of one or more Telegram
-- channels. It is a marketing mechanism, and the design here is shaped by that:
-- it can gate a purchase and it can suspend access, so every part of it is
-- reversible, warned about first, and auditable afterwards.

CREATE TABLE required_channels (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),

  -- The channel as Telegram identifies it. The numeric identifier is what
  -- getChatMember takes; the username is for the button the customer taps and
  -- may change without the channel changing.
  telegram_chat_id bigint NOT NULL UNIQUE,
  username text CHECK (username IS NULL OR username ~ '^[A-Za-z0-9_]{5,32}$'),
  title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 120),
  invite_url text CHECK (invite_url IS NULL OR char_length(invite_url) <= 400),

  enabled boolean NOT NULL DEFAULT false,

  -- Where membership is required. They are separate because they are different
  -- promises: gating a purchase asks somebody to join before they pay, and
  -- gating activation takes access away from somebody who already did.
  require_for_purchase boolean NOT NULL DEFAULT true,
  require_for_activation boolean NOT NULL DEFAULT false,

  sort_order integer NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid REFERENCES admin_users(id)
);

CREATE INDEX required_channels_active_idx
  ON required_channels (sort_order) WHERE enabled;

-- What the last check saw, per customer and channel.
--
-- Verification results are cached because getChatMember is a rate-limited call
-- against a third party, and re-asking on every screen would make the bot slower
-- for everybody to enforce a marketing rule. The cache is short and the recorded
-- time is what makes "when was this last true" answerable.
CREATE TABLE channel_memberships (
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  channel_id uuid NOT NULL REFERENCES required_channels(id) ON DELETE CASCADE,

  state text NOT NULL CHECK (state IN ('member', 'absent', 'unknown')),
  -- `unknown` is its own state rather than being folded into `absent`. Telegram
  -- being unreachable is not the customer having left, and treating it as such
  -- would suspend people because of an outage.
  checked_at timestamptz NOT NULL DEFAULT now(),
  left_at timestamptz,

  PRIMARY KEY (user_id, channel_id)
);

CREATE INDEX channel_memberships_stale_idx ON channel_memberships (checked_at);
CREATE INDEX channel_memberships_absent_idx
  ON channel_memberships (left_at) WHERE state = 'absent';

-- A customer who must not be gated.
--
-- Exemptions exist because the rule is a marketing mechanism and there are
-- always people it should not apply to: a paying customer who cannot join for
-- their own reasons, somebody the operator promised, a test account. It records
-- who granted it and why, because an exemption that nobody can explain is
-- indistinguishable from a mistake.
CREATE TABLE channel_exemptions (
  user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  reason text NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 400),
  granted_by uuid REFERENCES admin_users(id),
  granted_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz
);

-- The enforcement state of one customer, and the grace clock.
--
-- Leaving a channel does not suspend anything immediately. The customer is
-- warned, given a grace period, and restored automatically if they rejoin
-- inside it — because the common reason somebody leaves a channel is that they
-- did not realise it was load-bearing.
CREATE TABLE channel_enforcement (
  user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  state text NOT NULL DEFAULT 'compliant'
    CHECK (state IN ('compliant', 'warned', 'suspended', 'exempt')),
  warned_at timestamptz,
  grace_until timestamptz,
  suspended_at timestamptz,
  restored_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX channel_enforcement_due_idx
  ON channel_enforcement (grace_until) WHERE state = 'warned';

-- Operator-wide policy for the mechanism.
ALTER TABLE commerce_settings
  ADD COLUMN channel_grace_seconds bigint NOT NULL DEFAULT 259200
    CHECK (channel_grace_seconds >= 0),
  ADD COLUMN channel_recheck_seconds bigint NOT NULL DEFAULT 21600
    CHECK (channel_recheck_seconds >= 300);
