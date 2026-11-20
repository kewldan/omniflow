-- Limits on what add-ons may add, and how long a traffic pack lasts
-- (Bedolaga's MAX_DEVICES_LIMIT, max_topup_traffic_gb, and its thirty-day
-- traffic purchases).

-- The most devices any subscription may reach through add-ons. Null is none.
ALTER TABLE subscription_policy
  ADD COLUMN max_device_limit integer,
  ADD CONSTRAINT subscription_policy_max_device_limit_positive
    CHECK (max_device_limit IS NULL OR max_device_limit > 0);

-- The most traffic add-ons may add to one entitlement of this version. Null
-- is no cap.
ALTER TABLE plan_versions
  ADD COLUMN max_addon_traffic_bytes bigint,
  ADD CONSTRAINT plan_versions_max_addon_traffic_positive
    CHECK (max_addon_traffic_bytes IS NULL OR max_addon_traffic_bytes > 0);

-- How many days a traffic pack lasts from its purchase. Null lasts as long as
-- the entitlement it was added to, which is what every pack did before.
ALTER TABLE addon_versions
  ADD COLUMN lifetime_days integer,
  ADD CONSTRAINT addon_versions_lifetime_days_positive
    CHECK (lifetime_days IS NULL OR lifetime_days > 0);

-- When an applied pack stops counting, and when the maintenance pass took
-- it back off the entitlement. The second is set once, so a replayed pass
-- never subtracts twice.
ALTER TABLE entitlement_addons
  ADD COLUMN expires_at timestamptz,
  ADD COLUMN expired_at timestamptz;

CREATE INDEX entitlement_addons_due_expiry_idx
  ON entitlement_addons (expires_at) WHERE expires_at IS NOT NULL AND expired_at IS NULL;
