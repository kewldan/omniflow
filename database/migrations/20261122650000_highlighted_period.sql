-- The period a version recommends. The bot and the web checkout mark it when
-- they offer the version's periods, as the previous bot marked its featured
-- period. At most one per version and currency, so "the recommended one" is
-- never a choice between two.
ALTER TABLE plan_version_periods
  ADD COLUMN highlighted boolean NOT NULL DEFAULT false;

CREATE UNIQUE INDEX plan_version_periods_one_highlighted
  ON plan_version_periods (plan_version_id, currency) WHERE highlighted;
