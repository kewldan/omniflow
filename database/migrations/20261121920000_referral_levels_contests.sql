-- A second referral level and referral contests (Bedolaga's multi-level
-- referrals and referral contests), built on the existing rewards: a
-- second-level reward is a share of a first-level inviter reward, paid to the
-- inviter's own inviter, and reversed with it; a contest ranks inviters by
-- qualified referrals in a window and pays the places an operator set.
CREATE TABLE referral_level_settings (
    singleton boolean PRIMARY KEY DEFAULT true,
    level2_enabled boolean NOT NULL DEFAULT false,
    -- The second level's share of a first-level money reward, in percent.
    level2_percent integer NOT NULL DEFAULT 10,
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by uuid,
    CONSTRAINT referral_level_settings_singleton CHECK (singleton),
    CONSTRAINT referral_level_settings_percent_bounded CHECK (level2_percent BETWEEN 1 AND 50)
);

INSERT INTO referral_level_settings DEFAULT VALUES;

CREATE TABLE referral_level2_rewards (
    source_reward_id uuid PRIMARY KEY REFERENCES referral_rewards (id),
    beneficiary_user_id uuid NOT NULL REFERENCES users (id),
    amount_minor bigint NOT NULL,
    currency text NOT NULL,
    ledger_transaction_id uuid NOT NULL REFERENCES ledger_transactions (id),
    granted_at timestamptz NOT NULL DEFAULT now(),
    reversed_at timestamptz,
    reversal_ledger_transaction_id uuid REFERENCES ledger_transactions (id),
    CONSTRAINT referral_level2_rewards_amount_positive CHECK (amount_minor > 0),
    CONSTRAINT referral_level2_rewards_reversal_complete CHECK ((reversed_at IS NULL) = (reversal_ledger_transaction_id IS NULL))
);

CREATE TABLE referral_contests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title_ru text NOT NULL,
    title_en text NOT NULL,
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    currency text NOT NULL DEFAULT 'RUB',
    -- Prize per place, first place first, in minor units.
    prizes bigint[] NOT NULL,
    finished_at timestamptz,
    finished_by uuid,
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT referral_contests_titles_length CHECK (char_length(title_ru) BETWEEN 1 AND 120 AND char_length(title_en) BETWEEN 1 AND 120),
    CONSTRAINT referral_contests_window CHECK (ends_at > starts_at),
    CONSTRAINT referral_contests_prizes_shape CHECK (cardinality(prizes) BETWEEN 1 AND 10 AND 0 < ALL (prizes)),
    CONSTRAINT referral_contests_currency_iso CHECK (currency ~ '^[A-Z]{3}$')
);

CREATE TABLE referral_contest_winners (
    contest_id uuid NOT NULL REFERENCES referral_contests (id),
    place integer NOT NULL,
    user_id uuid NOT NULL REFERENCES users (id),
    referrals integer NOT NULL,
    amount_minor bigint NOT NULL,
    ledger_transaction_id uuid NOT NULL REFERENCES ledger_transactions (id),
    PRIMARY KEY (contest_id, place),
    CONSTRAINT referral_contest_winners_place_positive CHECK (place >= 1)
);
