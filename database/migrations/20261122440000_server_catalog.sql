-- The server catalogue (Bedolaga's server list): how Omniflow labels and
-- limits the sale of a Remnawave internal squad. Remnawave stays
-- authoritative for squads, nodes, and members (decision 0004): nothing here
-- creates, changes, or deletes one, and a squad without a row is sold exactly
-- as before.
CREATE TABLE server_catalog (
  -- The Remnawave internal squad this row describes.
  squad_id uuid NOT NULL,
  name_ru text NOT NULL,
  name_en text NOT NULL,
  -- ISO 3166-1 alpha-2, shown as a flag; NULL shows none.
  country_code text,
  -- The most customers a sale may put on the squad; NULL is no limit.
  max_users integer,
  -- Whether a trial may include the squad.
  trial_eligible boolean NOT NULL DEFAULT true,
  sort_order integer NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid,
  CONSTRAINT server_catalog_pkey PRIMARY KEY (squad_id),
  CONSTRAINT server_catalog_names_present CHECK (length(name_ru) BETWEEN 1 AND 80 AND length(name_en) BETWEEN 1 AND 80),
  CONSTRAINT server_catalog_country_shape CHECK (country_code IS NULL OR country_code ~ '^[A-Z]{2}$'),
  CONSTRAINT server_catalog_max_users_positive CHECK (max_users IS NULL OR max_users > 0)
);

-- What choosing the squad adds to a plan's price at checkout, per currency,
-- for the version's own duration; another period scales it in proportion.
-- Only a squad the customer chooses is charged, never one the plan includes.
CREATE TABLE server_catalog_prices (
  squad_id uuid NOT NULL REFERENCES server_catalog (squad_id) ON DELETE CASCADE,
  currency text NOT NULL,
  amount_minor bigint NOT NULL,
  CONSTRAINT server_catalog_prices_pkey PRIMARY KEY (squad_id, currency),
  CONSTRAINT server_catalog_prices_currency_shape CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT server_catalog_prices_amount_positive CHECK (amount_minor > 0)
);

-- Counting the customers on a squad reads live entitlements by squad.
CREATE INDEX entitlements_live_squads ON entitlements USING gin (remnawave_squad_ids)
  WHERE status IN ('pending', 'active', 'limited', 'paused');
