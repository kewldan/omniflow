-- Customer notices about events that happened to them rather than to their
-- subscription: somebody joined through their referral link, a referral reward
-- was credited, they arrived through an invite, their loyalty tier changed.
-- Bedolaga tells both sides of a referral; Omniflow had the `referral`
-- notification kind and nothing that queued it.
--
-- A notice is written in the same transaction as the event, by a trigger, so
-- every path that records the event — the bot, the web panel, a manual attach
-- by an operator — queues it. Delivery goes through notification_deliveries
-- under the dedupe key `event:<id>`, which is where the customer's preferences,
-- quiet hours, and the delivery outcome are recorded; this table holds only
-- what happened and the few parameters the wording needs.
CREATE TABLE customer_event_notices (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  kind text NOT NULL,
  params jsonb NOT NULL DEFAULT '{}'::jsonb,
  dedupe_key text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT customer_event_notices_kind_known CHECK (kind IN (
    'referral_joined', 'referral_welcome', 'referral_reward', 'loyalty_tier'
  )),
  CONSTRAINT customer_event_notices_params_object CHECK (jsonb_typeof(params) = 'object'),
  CONSTRAINT customer_event_notices_dedupe_unique UNIQUE (dedupe_key)
);
CREATE INDEX customer_event_notices_recent_idx ON customer_event_notices (created_at);

ALTER TABLE notification_deliveries DROP CONSTRAINT notification_deliveries_kind_known;
ALTER TABLE notification_deliveries ADD CONSTRAINT notification_deliveries_kind_known CHECK (kind IN (
  'expiry', 'traffic', 'renewal', 'grace', 'recovery', 'payment', 'fulfillment',
  'support', 'news', 'announcement', 'incident', 'maintenance', 'marketing',
  'referral', 'trial', 'test', 'connection', 'device', 'access', 'reminder', 'loyalty'
));

-- Operators hear about loyalty tier changes in their own topic.
ALTER TABLE operator_notifications DROP CONSTRAINT operator_notifications_kind_known;
ALTER TABLE operator_notifications ADD CONSTRAINT operator_notifications_kind_known CHECK (kind IN (
  'purchase', 'renewal', 'topup', 'refund', 'fulfillment_failure', 'incident',
  'backup', 'security', 'anomaly', 'support', 'trial', 'addon', 'promo', 'partner',
  'error', 'infrastructure', 'report', 'node', 'monitoring', 'marketing'
));
ALTER TABLE operator_topics DROP CONSTRAINT operator_topics_kind_known;
ALTER TABLE operator_topics ADD CONSTRAINT operator_topics_kind_known CHECK (kind IN (
  'purchase', 'renewal', 'topup', 'refund', 'fulfillment_failure', 'incident',
  'backup', 'security', 'anomaly', 'campaign_test', 'notice_test', 'support', 'trial',
  'addon', 'promo', 'partner', 'error', 'infrastructure', 'report', 'node', 'monitoring',
  'marketing'
));

-- A pair written live tells the inviter somebody joined and welcomes the
-- invitee. A pair imported already qualified is history, not news.
CREATE FUNCTION queue_referral_attribution_notices() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF NEW.qualified_at IS NOT NULL THEN
    RETURN NEW;
  END IF;
  INSERT INTO customer_event_notices (user_id, kind, dedupe_key)
  VALUES (NEW.referrer_user_id, 'referral_joined', 'referral-joined:' || NEW.referred_user_id::text),
         (NEW.referred_user_id, 'referral_welcome', 'referral-welcome:' || NEW.referred_user_id::text)
  ON CONFLICT (dedupe_key) DO NOTHING;
  RETURN NEW;
END;
$$;
CREATE TRIGGER referral_attributions_notice AFTER INSERT ON referral_attributions
  FOR EACH ROW EXECUTE FUNCTION queue_referral_attribution_notices();

CREATE FUNCTION queue_referral_reward_notice() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  INSERT INTO customer_event_notices (user_id, kind, params, dedupe_key)
  VALUES (NEW.beneficiary_user_id, 'referral_reward',
          jsonb_build_object('role', NEW.role, 'amountMinor', NEW.amount_minor,
                             'currency', NEW.currency, 'days', COALESCE(NEW.days, 0)),
          'referral-reward:' || NEW.id::text)
  ON CONFLICT (dedupe_key) DO NOTHING;
  RETURN NEW;
END;
$$;
CREATE TRIGGER referral_rewards_notice AFTER INSERT ON referral_rewards
  FOR EACH ROW EXECUTE FUNCTION queue_referral_reward_notice();

-- A tier change tells the customer their new tier and what it is worth, and
-- tells operators who moved, from where, to where, and why. The operator notice
-- carries identifiers and tier codes only.
CREATE FUNCTION queue_loyalty_change_notices() RETURNS trigger
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
                             'down', previous_threshold IS NOT NULL AND next_tier.threshold < previous_threshold),
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
CREATE TRIGGER loyalty_standing_history_notice AFTER INSERT ON loyalty_standing_history
  FOR EACH ROW EXECUTE FUNCTION queue_loyalty_change_notices();
