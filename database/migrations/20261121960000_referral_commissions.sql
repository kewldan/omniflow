-- Bedolaga pays the inviter a percentage of every payment the invitee makes,
-- with a separate rate for the first payment, rates that grow with the number
-- of friends invited, a cap on how many payments earn, a minimum payment, and
-- a choice of which payments count. Omniflow paid a fixed amount once.
--
-- A commission is a referral reward of role `commission`, one per order, so
-- every place that reads rewards — the customer's earnings, withdrawals,
-- review and reversal, notices — reads commissions too.
ALTER TABLE referral_programs ADD COLUMN commission_bps integer NOT NULL DEFAULT 0;
ALTER TABLE referral_programs ADD COLUMN first_payment_commission_bps integer;
ALTER TABLE referral_programs ADD COLUMN commission_payment_cap integer;
ALTER TABLE referral_programs ADD COLUMN commission_min_payment_minor bigint NOT NULL DEFAULT 0;
ALTER TABLE referral_programs ADD COLUMN commission_triggers text[] NOT NULL DEFAULT '{subscription}';
-- [{"referrals": 10, "bps": 1500}, …]: the rate once the inviter has that many
-- qualified referrals.
ALTER TABLE referral_programs ADD COLUMN commission_tiers jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE referral_programs ADD CONSTRAINT referral_programs_commission_bounded CHECK (
  commission_bps BETWEEN 0 AND 10000
  AND (first_payment_commission_bps IS NULL OR first_payment_commission_bps BETWEEN 0 AND 10000)
  AND (commission_payment_cap IS NULL OR commission_payment_cap > 0)
  AND commission_min_payment_minor >= 0
  AND commission_triggers <@ ARRAY['subscription', 'topup']::text[]
  AND jsonb_typeof(commission_tiers) = 'array'
);

ALTER TABLE referral_rewards DROP CONSTRAINT referral_rewards_role_check;
ALTER TABLE referral_rewards ADD CONSTRAINT referral_rewards_role_check CHECK (role IN ('inviter', 'invitee', 'commission'));
ALTER TABLE referral_rewards DROP CONSTRAINT referral_rewards_referred_user_id_role_key;
CREATE UNIQUE INDEX referral_rewards_one_per_role ON referral_rewards (referred_user_id, role) WHERE role <> 'commission';
CREATE UNIQUE INDEX referral_rewards_one_commission_per_order ON referral_rewards (order_id) WHERE role = 'commission';

-- A personal rate for one inviter — a partner, a blogger — overriding the
-- programme's rates and tiers.
CREATE TABLE referral_inviter_terms (
  user_id uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
  commission_bps integer,
  first_payment_commission_bps integer,
  note text NOT NULL DEFAULT '',
  updated_by uuid REFERENCES admin_users (id) ON DELETE SET NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT referral_inviter_terms_bounded CHECK (
    (commission_bps IS NULL OR commission_bps BETWEEN 0 AND 10000)
    AND (first_payment_commission_bps IS NULL OR first_payment_commission_bps BETWEEN 0 AND 10000)
  ),
  CONSTRAINT referral_inviter_terms_note_length CHECK (char_length(note) <= 400)
);
