-- Referral rewards in subscription days, the way Bedolaga installations run
-- their programmes ("+7 days to the friend, +14 to the inviter"). Per role, a
-- day reward takes precedence over a money reward when both are configured.
ALTER TABLE referral_programs ADD COLUMN inviter_reward_days integer NOT NULL DEFAULT 0;
ALTER TABLE referral_programs ADD COLUMN invitee_reward_days integer NOT NULL DEFAULT 0;
ALTER TABLE referral_programs ADD CONSTRAINT referral_programs_reward_days_bounded CHECK (
  inviter_reward_days BETWEEN 0 AND 3650 AND invitee_reward_days BETWEEN 0 AND 3650
);

-- A day reward moves no money: it carries days instead of an amount and a
-- ledger transaction, and its entitlement is found through order_id's
-- fulfillment like any other grant.
ALTER TABLE referral_rewards ADD COLUMN days integer;
ALTER TABLE referral_rewards ALTER COLUMN ledger_transaction_id DROP NOT NULL;
ALTER TABLE referral_rewards DROP CONSTRAINT referral_rewards_amount_minor_check;
ALTER TABLE referral_rewards ADD CONSTRAINT referral_rewards_reward_complete CHECK (
  (days IS NULL AND amount_minor > 0 AND ledger_transaction_id IS NOT NULL)
  OR (days > 0 AND amount_minor = 0 AND ledger_transaction_id IS NULL)
);
