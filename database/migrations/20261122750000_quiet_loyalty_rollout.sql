-- A tier change tells the customer and the operators about it, except when
-- there is nothing to tell:
--   * the tier grants nothing — no discount of any kind — so "your new tier"
--     would announce a name and no benefit;
--   * the programme is being rolled out: a customer's first placement within
--     a day of the programme's publication, when every customer is placed at
--     once. Bedolaga's promo groups carried over on 22 November did exactly
--     this, and 1 946 customers were told they had reached the base tier.
-- Promotions between tiers, and a first placement later on, still notify.
CREATE OR REPLACE FUNCTION queue_loyalty_change_notices() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
  previous_threshold bigint;
  next_tier loyalty_tiers%ROWTYPE;
  previous_code text;
  published timestamptz;
BEGIN
  SELECT * INTO next_tier FROM loyalty_tiers WHERE id = NEW.to_tier_id;
  SELECT threshold, code INTO previous_threshold, previous_code FROM loyalty_tiers WHERE id = NEW.from_tier_id;
  SELECT published_at INTO published FROM loyalty_programs WHERE id = next_tier.program_id;

  IF next_tier.discount_bps = 0 AND next_tier.traffic_discount_bps = 0
     AND next_tier.device_discount_bps = 0 AND next_tier.squad_discount_bps = 0
     AND next_tier.period_discounts = '{}'::jsonb THEN
    RETURN NEW;
  END IF;
  IF NEW.from_tier_id IS NULL AND NEW.reason = 'evaluation'
     AND published IS NOT NULL AND published > now() - interval '1 day' THEN
    RETURN NEW;
  END IF;

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
