-- Reward codes Bedolaga issues and Omniflow did not: wallet credit together
-- with days, traffic added to the current period, and a code that grants a
-- loyalty group (Bedolaga's promo group).
--
--   balance_days  value = days, credit_minor + currency = the wallet credit
--   traffic       value = gigabytes added to the current subscription period
--   loyalty_tier  value = days the group is held, tier_code = the group
ALTER TABLE promotions ADD COLUMN credit_minor bigint;
ALTER TABLE promotions ADD COLUMN tier_code text;

ALTER TABLE promotions DROP CONSTRAINT promotions_kind_known;
ALTER TABLE promotions ADD CONSTRAINT promotions_kind_known CHECK (kind IN (
  'percent', 'fixed', 'wallet_credit', 'days', 'trial', 'balance_days', 'traffic', 'loyalty_tier'
));
ALTER TABLE promotions ADD CONSTRAINT promotions_balance_days_complete CHECK (
  kind <> 'balance_days' OR (currency IS NOT NULL AND credit_minor IS NOT NULL AND credit_minor > 0 AND value <= 3650)
);
ALTER TABLE promotions ADD CONSTRAINT promotions_traffic_bounded CHECK (kind <> 'traffic' OR value <= 100000);
ALTER TABLE promotions ADD CONSTRAINT promotions_tier_complete CHECK (
  kind <> 'loyalty_tier' OR (tier_code ~ '^[a-z][a-z0-9_-]{1,31}$' AND value <= 3650)
);

-- How many codes of one wholesale batch a single customer may redeem
-- (Bedolaga's CouponBatch.max_per_user). Null is no limit.
ALTER TABLE code_batches ADD COLUMN max_per_customer integer;
ALTER TABLE code_batches ADD CONSTRAINT code_batches_max_per_customer_positive CHECK (
  max_per_customer IS NULL OR max_per_customer > 0
);
