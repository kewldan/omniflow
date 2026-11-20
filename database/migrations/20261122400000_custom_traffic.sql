-- A traffic volume of the customer's own choosing, priced per gigabyte
-- (Bedolaga's tariff_custom_traffic), beside the version's own allowance.
-- A gigabyte here is 2^30 bytes, as everywhere else Omniflow shows traffic.

-- The range the customer chooses in. The price per gigabyte is per currency,
-- in plan_version_gb_prices, and is for the version's own duration; a longer
-- or shorter period scales it in proportion.
ALTER TABLE plan_versions
  ADD COLUMN custom_traffic_min_gb integer,
  ADD COLUMN custom_traffic_max_gb integer,
  ADD CONSTRAINT plan_versions_custom_traffic_range CHECK (
    (custom_traffic_min_gb IS NULL) = (custom_traffic_max_gb IS NULL)
    AND (custom_traffic_min_gb IS NULL OR (custom_traffic_min_gb >= 1
      AND custom_traffic_max_gb >= custom_traffic_min_gb AND custom_traffic_max_gb <= 1000000))
  );

CREATE TABLE plan_version_gb_prices (
  plan_version_id uuid NOT NULL REFERENCES plan_versions (id) ON DELETE CASCADE,
  currency text NOT NULL,
  amount_minor bigint NOT NULL,
  PRIMARY KEY (plan_version_id, currency),
  CONSTRAINT plan_version_gb_prices_currency_shape CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT plan_version_gb_prices_amount_positive CHECK (amount_minor > 0)
);

-- The traffic an order bought, in gigabytes, when it is not the version's own
-- allowance. The entitlement it settles into carries exactly this allowance.
ALTER TABLE orders
  ADD COLUMN traffic_gb integer,
  ADD CONSTRAINT orders_traffic_gb_positive CHECK (traffic_gb IS NULL OR traffic_gb >= 1);

-- The volume chosen in an open checkout, shared by the web and the bot.
ALTER TABLE bot_checkout_sessions
  ADD COLUMN traffic_gb integer,
  ADD CONSTRAINT bot_checkout_sessions_traffic_gb_positive CHECK (traffic_gb IS NULL OR traffic_gb >= 1);
