-- Referral balance withdrawal (Bedolaga's "вывод реферального баланса").
--
-- A request holds its amount at once: a debit from the customer's wallet is
-- posted in the same transaction as the request, so money promised for a
-- payout cannot also be spent on a subscription. Rejecting or cancelling the
-- request posts the matching credit back; approving and marking it paid move
-- no ledger money, because the payout itself happens outside Omniflow.
CREATE TABLE referral_withdrawal_settings (
    singleton boolean PRIMARY KEY DEFAULT true,
    enabled boolean NOT NULL DEFAULT false,
    currency text NOT NULL DEFAULT 'RUB',
    minimum_minor bigint NOT NULL DEFAULT 50000,
    cooldown_days integer NOT NULL DEFAULT 30,
    -- Only what referrals earned may be withdrawn, never the customer's own
    -- top-ups: the shape of a laundering loop is "top up, withdraw".
    only_referral_balance boolean NOT NULL DEFAULT true,
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by uuid,
    CONSTRAINT referral_withdrawal_settings_singleton CHECK (singleton),
    CONSTRAINT referral_withdrawal_settings_currency_iso CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT referral_withdrawal_settings_minimum_positive CHECK (minimum_minor > 0),
    CONSTRAINT referral_withdrawal_settings_cooldown_bounded CHECK (cooldown_days BETWEEN 0 AND 365)
);

INSERT INTO referral_withdrawal_settings DEFAULT VALUES;

CREATE TABLE referral_withdrawals (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users (id),
    amount_minor bigint NOT NULL,
    currency text NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    payout_method text NOT NULL,
    -- Card number, phone for SBP, or wallet address: sealed with the data key.
    payout_details_ciphertext bytea NOT NULL,
    risk_score integer NOT NULL,
    risk_flags text[] NOT NULL DEFAULT '{}',
    risk_evidence jsonb NOT NULL DEFAULT '{}',
    hold_ledger_transaction_id uuid NOT NULL REFERENCES ledger_transactions (id),
    release_ledger_transaction_id uuid REFERENCES ledger_transactions (id),
    idempotency_key text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    decided_at timestamptz,
    decided_by uuid,
    decision_note text,
    paid_at timestamptz,
    paid_by uuid,
    CONSTRAINT referral_withdrawals_amount_positive CHECK (amount_minor > 0),
    CONSTRAINT referral_withdrawals_currency_iso CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT referral_withdrawals_status_known CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled', 'paid')),
    CONSTRAINT referral_withdrawals_method_known CHECK (payout_method IN ('card', 'sbp', 'crypto', 'other')),
    CONSTRAINT referral_withdrawals_risk_bounded CHECK (risk_score BETWEEN 0 AND 100),
    CONSTRAINT referral_withdrawals_note_length CHECK (decision_note IS NULL OR char_length(decision_note) <= 400),
    CONSTRAINT referral_withdrawals_decided_complete CHECK ((status = 'pending') = (decided_at IS NULL)),
    CONSTRAINT referral_withdrawals_release_complete CHECK (
        (status IN ('rejected', 'cancelled')) = (release_ledger_transaction_id IS NOT NULL)
    ),
    CONSTRAINT referral_withdrawals_paid_complete CHECK ((status = 'paid') = (paid_at IS NOT NULL)),
    CONSTRAINT referral_withdrawals_idempotency_unique UNIQUE (user_id, idempotency_key)
);

-- One open request per customer at a time.
CREATE UNIQUE INDEX referral_withdrawals_one_pending_idx ON referral_withdrawals (user_id) WHERE status = 'pending';
CREATE INDEX referral_withdrawals_queue_idx ON referral_withdrawals (status, created_at);
