-- The smart subscription: Omniflow serves the link a VPN client imports,
-- reads the user's subscription from Remnawave behind it, and rewrites it into
-- country groups with balancers, a whitelist-bypass fallback, and routing the
-- customer chooses. See internal/smartsub.
--
-- One row of operator settings, like simple_subscription. The document is
-- validated by internal/smartsub (Settings.Normalize) before it is written;
-- the column only guarantees it is an object. The version turns two panel
-- tabs saving at once into a conflict.
CREATE TABLE smart_subscription_settings (
  singleton boolean NOT NULL DEFAULT true,
  enabled boolean NOT NULL DEFAULT false,
  document jsonb NOT NULL DEFAULT '{}'::jsonb,
  version integer NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid,
  CONSTRAINT smart_subscription_settings_pkey PRIMARY KEY (singleton),
  CONSTRAINT smart_subscription_settings_singleton CHECK (singleton),
  CONSTRAINT smart_subscription_settings_document_object CHECK (jsonb_typeof(document) = 'object'),
  CONSTRAINT smart_subscription_settings_version_positive CHECK (version > 0)
);

INSERT INTO smart_subscription_settings (singleton) VALUES (true);

-- A customer's choices for one subscription. No row means the operator's
-- defaults. The customer is reached through the subscription, so a merge that
-- moves a subscription to another customer carries the choices with it.
CREATE TABLE smart_subscription_preferences (
  subscription_id uuid NOT NULL REFERENCES subscriptions (id) ON DELETE CASCADE,
  -- ISO 3166-1 codes, or ZZ for servers whose country is unknown.
  hidden_countries text[] NOT NULL DEFAULT '{}',
  auto_excluded text[] NOT NULL DEFAULT '{}',
  reserve boolean NOT NULL DEFAULT true,
  russia_direct boolean NOT NULL DEFAULT false,
  block_ads boolean NOT NULL DEFAULT false,
  block_quic boolean NOT NULL DEFAULT false,
  -- [{"domain": "example.com", "action": "direct" | "proxy" | "block"}]
  rules jsonb NOT NULL DEFAULT '[]'::jsonb,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT smart_subscription_preferences_pkey PRIMARY KEY (subscription_id),
  CONSTRAINT smart_subscription_preferences_countries_bounded
    CHECK (cardinality(hidden_countries) <= 300 AND cardinality(auto_excluded) <= 300),
  CONSTRAINT smart_subscription_preferences_rules_array
    CHECK (jsonb_typeof(rules) = 'array' AND jsonb_array_length(rules) <= 200)
);
