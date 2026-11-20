-- Engagement (Bedolaga's fortune wheel, daily games, and polls with rewards).
--
-- Every prize and reward is a ledger credit or a subscription extension
-- written through the same paths as any other grant, keyed so a retry cannot
-- pay twice; every spin and answer is kept as its own row, which is the audit.
CREATE TABLE engagement_settings (
    singleton boolean PRIMARY KEY DEFAULT true,
    currency text NOT NULL DEFAULT 'RUB',
    -- The plan whose duration a days prize or reward extends.
    days_plan_version_id uuid REFERENCES plan_versions (id),
    -- What one prize day is worth, for the RTP calculation only.
    day_value_minor bigint NOT NULL DEFAULT 1000,
    wheel_enabled boolean NOT NULL DEFAULT false,
    -- A paid spin is charged to the wallet; zero means spins are free.
    wheel_spin_cost_minor bigint NOT NULL DEFAULT 0,
    -- Abuse limits: free spins and total spins per customer per UTC day.
    wheel_free_spins_per_day integer NOT NULL DEFAULT 1,
    wheel_max_spins_per_day integer NOT NULL DEFAULT 5,
    -- The highest expected payout, as a percentage of the spin cost, the
    -- prize table may have. The panel refuses a table above it.
    wheel_target_rtp_percent integer NOT NULL DEFAULT 80,
    -- Customers younger than this cannot spin or be rewarded for a poll.
    minimum_account_age_hours integer NOT NULL DEFAULT 24,
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by uuid,
    CONSTRAINT engagement_settings_singleton CHECK (singleton),
    CONSTRAINT engagement_settings_currency_iso CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT engagement_settings_day_value_positive CHECK (day_value_minor > 0),
    CONSTRAINT engagement_settings_spin_cost_nonnegative CHECK (wheel_spin_cost_minor >= 0),
    CONSTRAINT engagement_settings_spin_limits CHECK (
        wheel_free_spins_per_day BETWEEN 0 AND 10 AND wheel_max_spins_per_day BETWEEN 1 AND 100
        AND wheel_free_spins_per_day <= wheel_max_spins_per_day
    ),
    CONSTRAINT engagement_settings_rtp_bounded CHECK (wheel_target_rtp_percent BETWEEN 1 AND 100),
    CONSTRAINT engagement_settings_account_age_bounded CHECK (minimum_account_age_hours BETWEEN 0 AND 8760)
);

INSERT INTO engagement_settings DEFAULT VALUES;

CREATE TABLE wheel_prizes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    label_ru text NOT NULL,
    label_en text NOT NULL,
    kind text NOT NULL,
    value bigint NOT NULL DEFAULT 0,
    weight integer NOT NULL,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT wheel_prizes_kind_known CHECK (kind IN ('wallet', 'days', 'nothing')),
    CONSTRAINT wheel_prizes_value_matches_kind CHECK (
        (kind = 'nothing' AND value = 0) OR (kind = 'wallet' AND value > 0) OR (kind = 'days' AND value BETWEEN 1 AND 365)
    ),
    CONSTRAINT wheel_prizes_weight_positive CHECK (weight BETWEEN 1 AND 1000000),
    CONSTRAINT wheel_prizes_labels_length CHECK (char_length(label_ru) BETWEEN 1 AND 60 AND char_length(label_en) BETWEEN 1 AND 60)
);

CREATE TABLE wheel_spins (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users (id),
    prize_id uuid REFERENCES wheel_prizes (id),
    prize_kind text NOT NULL,
    prize_value bigint NOT NULL,
    cost_minor bigint NOT NULL,
    currency text NOT NULL,
    free boolean NOT NULL,
    idempotency_key text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT wheel_spins_kind_known CHECK (prize_kind IN ('wallet', 'days', 'nothing')),
    CONSTRAINT wheel_spins_cost_nonnegative CHECK (cost_minor >= 0),
    CONSTRAINT wheel_spins_idempotency_unique UNIQUE (user_id, idempotency_key)
);

CREATE INDEX wheel_spins_user_day_idx ON wheel_spins (user_id, created_at);

CREATE TABLE polls (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    question_ru text NOT NULL,
    question_en text NOT NULL,
    options jsonb NOT NULL,
    reward_kind text NOT NULL DEFAULT 'none',
    reward_value bigint NOT NULL DEFAULT 0,
    starts_at timestamptz NOT NULL DEFAULT now(),
    ends_at timestamptz NOT NULL,
    closed_at timestamptz,
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT polls_question_length CHECK (char_length(question_ru) BETWEEN 1 AND 300 AND char_length(question_en) BETWEEN 1 AND 300),
    CONSTRAINT polls_options_shape CHECK (jsonb_typeof(options) = 'array' AND jsonb_array_length(options) BETWEEN 2 AND 10),
    CONSTRAINT polls_reward_matches_kind CHECK (
        (reward_kind = 'none' AND reward_value = 0) OR (reward_kind = 'wallet' AND reward_value > 0)
        OR (reward_kind = 'days' AND reward_value BETWEEN 1 AND 30)
    ),
    CONSTRAINT polls_window CHECK (ends_at > starts_at)
);

CREATE TABLE poll_answers (
    poll_id uuid NOT NULL REFERENCES polls (id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users (id),
    option_index integer NOT NULL,
    rewarded boolean NOT NULL DEFAULT false,
    answered_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (poll_id, user_id),
    CONSTRAINT poll_answers_option_nonnegative CHECK (option_index >= 0)
);
