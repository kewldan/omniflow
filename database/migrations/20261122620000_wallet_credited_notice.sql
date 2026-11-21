-- A customer is told when a payment they started in the previous bot and paid
-- after the migration has been credited to their wallet.
ALTER TABLE customer_event_notices
  DROP CONSTRAINT customer_event_notices_kind_known,
  ADD CONSTRAINT customer_event_notices_kind_known CHECK (kind IN (
    'referral_joined', 'referral_welcome', 'referral_reward', 'loyalty_tier',
    'renewal_low_balance', 'daily_low_balance', 'daily_suspended', 'wallet_credited'
  ));
