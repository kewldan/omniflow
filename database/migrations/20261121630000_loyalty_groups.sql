-- Loyalty tiers become Bedolaga's promo groups.
--
-- A tier may discount the plan by billing period, and add-ons by kind — traffic,
-- devices, squads — separately from its plan discount. A tier may be
-- manual-only: never reached by the metric, held only when an operator or a
-- promo code grants it. When a customer holds several, the one with the highest
-- priority prices their orders. A tier may also restrict what its members can
-- buy and how they can pay: an empty list means no restriction.
ALTER TABLE loyalty_tiers ADD COLUMN traffic_discount_bps integer NOT NULL DEFAULT 0;
ALTER TABLE loyalty_tiers ADD COLUMN device_discount_bps integer NOT NULL DEFAULT 0;
ALTER TABLE loyalty_tiers ADD COLUMN squad_discount_bps integer NOT NULL DEFAULT 0;
ALTER TABLE loyalty_tiers ADD COLUMN period_discounts jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE loyalty_tiers ADD COLUMN priority integer NOT NULL DEFAULT 0;
ALTER TABLE loyalty_tiers ADD COLUMN manual_only boolean NOT NULL DEFAULT false;
ALTER TABLE loyalty_tiers ADD COLUMN allowed_plan_ids uuid[] NOT NULL DEFAULT '{}';
ALTER TABLE loyalty_tiers ADD COLUMN allowed_squad_ids uuid[] NOT NULL DEFAULT '{}';
ALTER TABLE loyalty_tiers ADD COLUMN allowed_providers text[] NOT NULL DEFAULT '{}';
ALTER TABLE loyalty_tiers ADD CONSTRAINT loyalty_tiers_addon_discounts_bounded CHECK (
  traffic_discount_bps BETWEEN 0 AND 10000
  AND device_discount_bps BETWEEN 0 AND 10000
  AND squad_discount_bps BETWEEN 0 AND 10000
);
ALTER TABLE loyalty_tiers ADD CONSTRAINT loyalty_tiers_period_discounts_object CHECK (
  jsonb_typeof(period_discounts) = 'object'
);
-- Thresholds stay unique along the ladder only: a manual-only group has no
-- place on it, so several may share the unused zero.
ALTER TABLE loyalty_tiers DROP CONSTRAINT loyalty_tiers_program_id_threshold_key;
CREATE UNIQUE INDEX loyalty_tiers_program_threshold_ladder_idx
  ON loyalty_tiers (program_id, threshold) WHERE NOT manual_only;

-- A group an operator or a promo code granted. It names the tier by code, so
-- it survives the next version of the programme as long as that code does.
CREATE TABLE loyalty_memberships (
  user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  tier_code text NOT NULL,
  source text NOT NULL,
  assigned_by uuid REFERENCES admin_users (id) ON DELETE SET NULL,
  note text NOT NULL DEFAULT '',
  expires_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, tier_code),
  CONSTRAINT loyalty_memberships_code_shape CHECK (tier_code ~ '^[a-z][a-z0-9_-]{1,31}$'),
  CONSTRAINT loyalty_memberships_source_known CHECK (source IN ('operator', 'promo_code')),
  CONSTRAINT loyalty_memberships_note_length CHECK (char_length(note) <= 400)
);

ALTER TABLE loyalty_standing_history DROP CONSTRAINT loyalty_standing_history_reason_check;
ALTER TABLE loyalty_standing_history ADD CONSTRAINT loyalty_standing_history_reason_check CHECK (
  reason IN ('evaluation', 'operator_override', 'program_change', 'promo_code')
);

-- Bulk recalculation: an operator asks, and the worker's loyalty pass
-- re-evaluates every customer once and records when it finished.
ALTER TABLE loyalty_programs ADD COLUMN recalculation_requested_at timestamptz;
ALTER TABLE loyalty_programs ADD COLUMN recalculated_at timestamptz;

-- A granted group is neither a promotion nor a demotion along the ladder, so
-- the customer is told the group's name and discount without "down" wording.
CREATE OR REPLACE FUNCTION queue_loyalty_change_notices() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
  previous_threshold bigint;
  next_tier loyalty_tiers%ROWTYPE;
  previous_code text;
BEGIN
  SELECT * INTO next_tier FROM loyalty_tiers WHERE id = NEW.to_tier_id;
  SELECT threshold, code INTO previous_threshold, previous_code FROM loyalty_tiers WHERE id = NEW.from_tier_id;
  INSERT INTO customer_event_notices (user_id, kind, params, dedupe_key)
  VALUES (NEW.user_id, 'loyalty_tier',
          jsonb_build_object('tierCode', next_tier.code, 'nameEn', next_tier.name_en,
                             'nameRu', next_tier.name_ru, 'discountBps', next_tier.discount_bps,
                             'down', NEW.reason = 'evaluation' AND NOT next_tier.manual_only
                                     AND previous_threshold IS NOT NULL
                                     AND next_tier.threshold < previous_threshold),
          'loyalty:' || NEW.id::text)
  ON CONFLICT (dedupe_key) DO NOTHING;
  INSERT INTO operator_notifications (kind, dedupe_key, payload)
  VALUES ('marketing', 'loyalty:' || NEW.id::text,
          jsonb_build_object('event', 'loyalty_tier', 'customerId', NEW.user_id::text,
                             'fromTier', COALESCE(previous_code, ''), 'toTier', next_tier.code,
                             'reason', NEW.reason))
  ON CONFLICT (kind, dedupe_key) DO NOTHING;
  RETURN NEW;
END;
$$;
