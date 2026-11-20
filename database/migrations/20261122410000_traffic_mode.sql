-- How a plan version sells traffic (Bedolaga's TRAFFIC_SELECTION_MODE):
--   fixed             the version's own allowance, and no traffic add-ons;
--   fixed_with_topup  the version's own allowance, and the traffic add-ons
--                     the version offers can top it up later;
--   selectable        the customer chooses at checkout from the version's
--                     traffic packages, or a custom volume priced per
--                     gigabyte, and may top it up later.
-- Every existing version keeps what it did: add-ons that top up traffic, and a
-- custom volume where one was published.
ALTER TABLE plan_versions
  ADD COLUMN traffic_mode text NOT NULL DEFAULT 'fixed_with_topup',
  ADD CONSTRAINT plan_versions_traffic_mode_known CHECK (traffic_mode IN ('fixed', 'fixed_with_topup', 'selectable'));

UPDATE plan_versions SET traffic_mode = 'selectable' WHERE custom_traffic_min_gb IS NOT NULL;

-- A traffic volume a selectable version is sold with, priced per currency
-- for the version's own duration; another period scales the price in
-- proportion. The volume replaces the version's own allowance.
CREATE TABLE plan_version_traffic_packages (
  plan_version_id uuid NOT NULL REFERENCES plan_versions (id) ON DELETE CASCADE,
  traffic_gb integer NOT NULL,
  currency text NOT NULL,
  amount_minor bigint NOT NULL,
  PRIMARY KEY (plan_version_id, traffic_gb, currency),
  CONSTRAINT plan_version_traffic_packages_volume_range CHECK (traffic_gb BETWEEN 1 AND 1000000),
  CONSTRAINT plan_version_traffic_packages_currency_shape CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT plan_version_traffic_packages_amount_nonnegative CHECK (amount_minor >= 0)
);
