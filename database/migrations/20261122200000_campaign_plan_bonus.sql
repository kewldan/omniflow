-- A /start campaign may now grant the installation's trial, or days of a plan
-- the operator picks, as well as wallet credit or days on the trial plan —
-- Bedolaga's "balance, subscription, or tariff" registration bonus.
--
-- `trial` claims the customer's one trial (trial_claims), so a customer who
-- already had one gets the attribution without the bonus. `plan` names a plan
-- rather than a version: the newest live version is resolved when the bonus is
-- granted, so republishing the plan does not strand the campaign on a retired
-- version. Both carry their length in days in bonus_value.
ALTER TABLE ad_campaigns ADD COLUMN bonus_plan_id uuid REFERENCES plans (id);

ALTER TABLE ad_campaigns DROP CONSTRAINT ad_campaigns_bonus_known;
ALTER TABLE ad_campaigns ADD CONSTRAINT ad_campaigns_bonus_known
  CHECK (bonus_kind IN ('none', 'wallet', 'days', 'trial', 'plan'));

ALTER TABLE ad_campaigns DROP CONSTRAINT ad_campaigns_bonus_value;
ALTER TABLE ad_campaigns ADD CONSTRAINT ad_campaigns_bonus_value CHECK (
  (bonus_kind = 'none' AND bonus_value = 0) OR (bonus_kind = 'wallet' AND bonus_value > 0)
  OR (bonus_kind IN ('days', 'trial', 'plan') AND bonus_value BETWEEN 1 AND 365)
);

ALTER TABLE ad_campaigns ADD CONSTRAINT ad_campaigns_bonus_plan_matches_kind
  CHECK ((bonus_kind = 'plan') = (bonus_plan_id IS NOT NULL));

