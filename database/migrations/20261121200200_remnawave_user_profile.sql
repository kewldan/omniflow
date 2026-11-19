-- What a plan asks Remnawave to do with a user beyond the expiry and the
-- limits: when the traffic counter resets on its own, which external squad
-- shapes the subscription page, and which tag the user carries in the panel.

-- Remnawave's own trafficLimitStrategy. NO_RESET is what every user was
-- created with before the column existed.
ALTER TABLE plan_versions
  ADD COLUMN traffic_reset_strategy text NOT NULL DEFAULT 'NO_RESET',
  ADD COLUMN external_squad_id uuid,
  ADD CONSTRAINT plan_versions_traffic_reset_strategy_known
    CHECK (traffic_reset_strategy IN ('NO_RESET', 'DAY', 'WEEK', 'MONTH', 'MONTH_ROLLING'));

-- A Remnawave user tag: the panel accepts upper-case letters, digits, and the
-- underscore, sixteen at most.
ALTER TABLE plans
  ADD COLUMN remnawave_tag text,
  ADD CONSTRAINT plans_remnawave_tag_shape CHECK (remnawave_tag ~ '^[A-Z0-9_]{1,16}$');

-- The tags a user carries when its plan names none: one while on a trial,
-- the other once it has paid.
ALTER TABLE subscription_policy
  ADD COLUMN trial_tag text,
  ADD COLUMN paid_tag text,
  ADD CONSTRAINT subscription_policy_trial_tag_shape CHECK (trial_tag ~ '^[A-Z0-9_]{1,16}$'),
  ADD CONSTRAINT subscription_policy_paid_tag_shape CHECK (paid_tag ~ '^[A-Z0-9_]{1,16}$');
