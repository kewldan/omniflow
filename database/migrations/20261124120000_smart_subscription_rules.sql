-- Service rules for the smart subscription: a customer's choices on the
-- operator's rules (by rule ID), the server they pinned, and the whitelist
-- mode. The three preset columns stay for the version that is still running
-- while this one rolls out; nothing writes them any more.
ALTER TABLE smart_subscription_preferences
  ADD COLUMN toggles jsonb NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN pinned_host uuid,
  ADD COLUMN whitelist_mode boolean NOT NULL DEFAULT false,
  ADD CONSTRAINT smart_subscription_preferences_toggles_object CHECK (jsonb_typeof(toggles) = 'object');

UPDATE smart_subscription_preferences
SET toggles = jsonb_build_object('russia', russia_direct, 'ads', block_ads, 'quic', block_quic);

-- Icons the operator uploads for service rules. They are served by the API,
-- never loaded from a third party, and small: a card shows them at 36 px.
CREATE TABLE smart_subscription_icons (
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  content_type text NOT NULL,
  data bytea NOT NULL,
  sha256 bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid,
  CONSTRAINT smart_subscription_icons_pkey PRIMARY KEY (id),
  CONSTRAINT smart_subscription_icons_sha256_key UNIQUE (sha256),
  CONSTRAINT smart_subscription_icons_content_type_known
    CHECK (content_type IN ('image/png', 'image/svg+xml', 'image/webp', 'image/jpeg')),
  CONSTRAINT smart_subscription_icons_size CHECK (octet_length(data) BETWEEN 1 AND 131072),
  CONSTRAINT smart_subscription_icons_sha256_length CHECK (octet_length(sha256) = 32)
);

-- A country every server of which is down: one row per outage, so the bot
-- tells customers once when it starts and once when it ends.
CREATE TABLE smart_subscription_outages (
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  country text NOT NULL,
  started_at timestamptz NOT NULL,
  ended_at timestamptz,
  announced_at timestamptz,
  CONSTRAINT smart_subscription_outages_pkey PRIMARY KEY (id),
  CONSTRAINT smart_subscription_outages_country_format CHECK (country ~ '^[A-Z]{2}$'),
  CONSTRAINT smart_subscription_outages_ended_after_start CHECK (ended_at IS NULL OR ended_at >= started_at)
);

-- At most one open outage per country.
CREATE UNIQUE INDEX smart_subscription_outages_open_idx
  ON smart_subscription_outages (country) WHERE ended_at IS NULL;

-- Who was told about an outage, so a recovery message only goes to the
-- people who heard about the outage, and nobody hears twice.
CREATE TABLE smart_subscription_outage_notices (
  outage_id uuid NOT NULL REFERENCES smart_subscription_outages (id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  kind text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  sent_at timestamptz,
  CONSTRAINT smart_subscription_outage_notices_pkey PRIMARY KEY (outage_id, user_id, kind),
  CONSTRAINT smart_subscription_outage_notices_kind_known CHECK (kind IN ('down', 'up'))
);

CREATE INDEX smart_subscription_outage_notices_pending_idx
  ON smart_subscription_outage_notices (created_at) WHERE sent_at IS NULL;

-- Customers can turn the server notices off in the bot and the cabinet.
ALTER TABLE bot_preferences
  ADD COLUMN server_notifications boolean NOT NULL DEFAULT true;
