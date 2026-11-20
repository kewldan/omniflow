-- One plan version, several billing periods (Bedolaga's period_prices and
-- AVAILABLE_RENEWAL_PERIODS), and a period of the customer's own choosing
-- priced by the day (tariff_custom_days).

-- A period a version is also sold for, beside its own duration and price.
-- `for_purchase` offers it on a first purchase or a change, `for_renewal` on an
-- extension or an automatic renewal; an operator offers renewals only in the
-- longer periods by clearing the first flag.
CREATE TABLE plan_version_periods (
  plan_version_id uuid NOT NULL REFERENCES plan_versions (id) ON DELETE CASCADE,
  days integer NOT NULL,
  currency text NOT NULL,
  amount_minor bigint NOT NULL,
  for_purchase boolean NOT NULL DEFAULT true,
  for_renewal boolean NOT NULL DEFAULT true,
  PRIMARY KEY (plan_version_id, days, currency),
  CONSTRAINT plan_version_periods_days_range CHECK (days BETWEEN 1 AND 3650),
  CONSTRAINT plan_version_periods_currency_shape CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT plan_version_periods_amount_nonnegative CHECK (amount_minor >= 0),
  CONSTRAINT plan_version_periods_offered CHECK (for_purchase OR for_renewal)
);

-- A number of days the customer types, between a minimum and a maximum, at a
-- price per day in each currency.
ALTER TABLE plan_versions
  ADD COLUMN custom_days_min integer,
  ADD COLUMN custom_days_max integer,
  ADD CONSTRAINT plan_versions_custom_days_range CHECK (
    (custom_days_min IS NULL) = (custom_days_max IS NULL)
    AND (custom_days_min IS NULL OR (custom_days_min >= 1 AND custom_days_max >= custom_days_min AND custom_days_max <= 3650))
  );

CREATE TABLE plan_version_day_prices (
  plan_version_id uuid NOT NULL REFERENCES plan_versions (id) ON DELETE CASCADE,
  currency text NOT NULL,
  amount_minor bigint NOT NULL,
  PRIMARY KEY (plan_version_id, currency),
  CONSTRAINT plan_version_day_prices_currency_shape CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT plan_version_day_prices_amount_positive CHECK (amount_minor > 0)
);

-- The period an order bought, when it is not the version's own duration. The
-- entitlement it settles into runs for exactly this long.
ALTER TABLE orders
  ADD COLUMN period_seconds bigint,
  ADD CONSTRAINT orders_period_seconds_positive CHECK (period_seconds IS NULL OR period_seconds > 0);

-- The period chosen in an open checkout, shared by the web and the bot.
ALTER TABLE bot_checkout_sessions
  ADD COLUMN period_days integer,
  ADD CONSTRAINT bot_checkout_sessions_period_days_range CHECK (period_days IS NULL OR period_days BETWEEN 1 AND 3650);
