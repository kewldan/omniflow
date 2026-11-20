-- "Simple subscription" (Bedolaga's SIMPLE_SUBSCRIPTION_*): an operator
-- setting that makes the bot and the web store show one plan with one Buy
-- button, its period, traffic, and extra devices chosen in advance, instead
-- of the catalogue and the configurator. One row, like subscription_policy.
CREATE TABLE simple_subscription (
  singleton boolean NOT NULL DEFAULT true,
  enabled boolean NOT NULL DEFAULT false,
  -- The plan sold; its newest live version is what the Buy button opens.
  plan_id uuid REFERENCES plans (id) ON DELETE SET NULL,
  -- A period other than the version's own, in days; NULL is its own.
  period_days integer,
  -- A traffic volume other than the version's own, in gigabytes; NULL is
  -- its own. The version must sell it (traffic chosen at checkout).
  traffic_gb integer,
  -- Extra devices: a devices add-on bought with the plan, and how many.
  device_addon_id uuid REFERENCES addons (id) ON DELETE SET NULL,
  device_quantity integer NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid,
  CONSTRAINT simple_subscription_pkey PRIMARY KEY (singleton),
  CONSTRAINT simple_subscription_singleton CHECK (singleton),
  CONSTRAINT simple_subscription_plan_when_enabled CHECK (NOT enabled OR plan_id IS NOT NULL),
  CONSTRAINT simple_subscription_period_range CHECK (period_days IS NULL OR period_days BETWEEN 1 AND 3650),
  CONSTRAINT simple_subscription_traffic_range CHECK (traffic_gb IS NULL OR traffic_gb BETWEEN 1 AND 1000000),
  CONSTRAINT simple_subscription_device_quantity_range CHECK (device_quantity BETWEEN 0 AND 100),
  CONSTRAINT simple_subscription_devices_named CHECK (device_quantity = 0 OR device_addon_id IS NOT NULL)
);

INSERT INTO simple_subscription (singleton) VALUES (true);
