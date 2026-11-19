-- Installation-wide rules for what a paid change does to a subscription, set
-- from the panel. One row, seeded with what the code did before the switches
-- existed, so an upgrade changes nothing until an operator decides otherwise.
CREATE TABLE subscription_policy (
  singleton boolean PRIMARY KEY DEFAULT true,
  -- A plan version's own upgrade and downgrade policies still apply; these
  -- turn either kind of change off for the whole catalogue at once.
  upgrades_enabled boolean NOT NULL DEFAULT true,
  downgrades_enabled boolean NOT NULL DEFAULT true,
  -- Whether paying for a new period starts the traffic counter afresh. An
  -- extension or renewal is the first; an upgrade or downgrade the second.
  reset_traffic_on_renewal boolean NOT NULL DEFAULT true,
  reset_traffic_on_plan_change boolean NOT NULL DEFAULT true,
  -- Whether a renewal removes every device the subscription has registered,
  -- so the customer reconnects within the new period's limit.
  reset_devices_on_renewal boolean NOT NULL DEFAULT false,
  -- What happens to the time left on a trial when the customer moves to a
  -- paid plan: the paid version's own upgrade policy, carried over, or
  -- dropped in favour of a period starting now.
  trial_remainder text NOT NULL DEFAULT 'policy',
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid REFERENCES admin_users (id) ON DELETE SET NULL,
  CONSTRAINT subscription_policy_singleton CHECK (singleton),
  CONSTRAINT subscription_policy_trial_remainder_known
    CHECK (trial_remainder IN ('policy', 'carry', 'reset'))
);

INSERT INTO subscription_policy DEFAULT VALUES;

COMMENT ON TABLE subscription_policy IS
  'Installation-wide switches for plan changes and what a paid period resets. One row, edited from the panel.';
