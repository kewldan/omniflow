-- Daily plans (Bedolaga's daily subscriptions): a version billed one day at a
-- time from the wallet by the worker, which the customer may pause.
--
-- A daily version is a one-day version: its own period is the day it charges
-- for. It sells no other period, so one charge is always one day.
ALTER TABLE plan_versions
  ADD COLUMN daily_billing boolean NOT NULL DEFAULT false,
  ADD CONSTRAINT plan_versions_daily_billing_day CHECK (
    NOT daily_billing OR (billing_period = 'day' AND duration_seconds = 86400 AND custom_days_min IS NULL)
  );

-- The worker charges each day under the key daily:<subscription>:<date the
-- day starts>, which the orders' (user_id, idempotency_key) unique constraint
-- makes one order per subscription and day.

-- The two things the customer hears about a daily plan: the balance will not
-- cover tomorrow, and access stopped because it did not.
ALTER TABLE customer_event_notices DROP CONSTRAINT customer_event_notices_kind_known;
ALTER TABLE customer_event_notices ADD CONSTRAINT customer_event_notices_kind_known CHECK (kind IN (
  'referral_joined', 'referral_welcome', 'referral_reward', 'loyalty_tier', 'renewal_low_balance',
  'daily_low_balance', 'daily_suspended'
));
