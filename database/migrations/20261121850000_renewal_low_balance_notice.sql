-- A wallet-funded automatic renewal that the balance will not cover is
-- announced ahead of its charge, so the customer can top up before the first
-- attempt fails (Bedolaga's AUTOPAY_WARNING_DAYS / LOW_BALANCE_ALERT).
ALTER TABLE customer_event_notices DROP CONSTRAINT customer_event_notices_kind_known;
ALTER TABLE customer_event_notices ADD CONSTRAINT customer_event_notices_kind_known CHECK (kind IN (
  'referral_joined', 'referral_welcome', 'referral_reward', 'loyalty_tier', 'renewal_low_balance'
));
