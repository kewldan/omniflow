-- The period an automatic renewal buys, chosen by the customer (Bedolaga's
-- DEFAULT_AUTOPAY_PERIOD_DAYS, but per subscription). NULL keeps the earlier
-- rule: a renewal repeats the period the subscription was last bought for.
-- A chosen period is checked against the plan version's renewal periods when
-- it is set and again when the renewal order is opened, so a period the
-- operator later withdraws falls back to that rule instead of failing.
ALTER TABLE auto_renew_settings
  ADD COLUMN period_days integer,
  ADD CONSTRAINT auto_renew_settings_period_days_range CHECK (period_days IS NULL OR period_days BETWEEN 1 AND 3650);
