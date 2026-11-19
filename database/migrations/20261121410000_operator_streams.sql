-- Operator notifications gain the streams Bedolaga operators are used to:
-- trials, add-on purchases, promo redemptions, partner (referral) rewards,
-- application errors, infrastructure events, and scheduled reports. The
-- anomaly evaluator already wrote `anomaly` notices; they now get a topic.
ALTER TABLE operator_notifications DROP CONSTRAINT operator_notifications_kind_known;
ALTER TABLE operator_notifications ADD CONSTRAINT operator_notifications_kind_known CHECK (
  kind = ANY (ARRAY[
    'purchase', 'renewal', 'topup', 'refund', 'fulfillment_failure', 'incident', 'backup',
    'security', 'anomaly', 'support',
    'trial', 'addon', 'promo', 'partner', 'error', 'infrastructure', 'report'
  ]::text[])
);
ALTER TABLE operator_topics DROP CONSTRAINT operator_topics_kind_known;
ALTER TABLE operator_topics ADD CONSTRAINT operator_topics_kind_known CHECK (
  kind = ANY (ARRAY[
    'purchase', 'renewal', 'topup', 'refund', 'fulfillment_failure', 'incident', 'backup',
    'security', 'anomaly', 'campaign_test', 'notice_test', 'support',
    'trial', 'addon', 'promo', 'partner', 'error', 'infrastructure', 'report'
  ]::text[])
);

-- Each stream is queued by a trigger on the row that records the event, so a
-- notice exists exactly when the event committed, whichever process wrote it.
-- Like every operator notice they carry identifiers and amounts, never a
-- customer's name, Telegram account, or text.
CREATE FUNCTION queue_operator_stream_notice() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF current_setting('omniflow.import', true) = 'on' THEN
    RETURN NEW;
  END IF;
  IF TG_TABLE_NAME = 'trial_claims' THEN
    INSERT INTO operator_notifications (kind, dedupe_key, payload)
    VALUES ('trial', 'trial:' || NEW.order_id::text,
            jsonb_build_object('orderId', NEW.order_id::text, 'event', 'trial_claimed'))
    ON CONFLICT (kind, dedupe_key) DO NOTHING;
  ELSIF TG_TABLE_NAME = 'promo_redemptions' THEN
    INSERT INTO operator_notifications (kind, dedupe_key, payload)
    VALUES ('promo', 'promo:' || NEW.id::text,
            jsonb_build_object('orderId', NEW.order_id::text, 'amountMinor', NEW.discount_minor,
                               'event', 'promo_redeemed'))
    ON CONFLICT (kind, dedupe_key) DO NOTHING;
  ELSIF TG_TABLE_NAME = 'referral_rewards' THEN
    INSERT INTO operator_notifications (kind, dedupe_key, payload)
    VALUES ('partner', 'reward:' || NEW.id::text,
            jsonb_build_object('orderId', NEW.order_id::text, 'amountMinor', NEW.amount_minor,
                               'currency', NEW.currency, 'event', 'referral_reward_' || NEW.role))
    ON CONFLICT (kind, dedupe_key) DO NOTHING;
  ELSIF TG_TABLE_NAME = 'outbox_events' AND NEW.topic = 'addon.purchased' THEN
    INSERT INTO operator_notifications (kind, dedupe_key, payload)
    VALUES ('addon', 'addon:' || NEW.id::text,
            jsonb_build_object('orderId', NEW.payload ->> 'orderId',
                               'entitlementId', NEW.payload ->> 'entitlementId',
                               'event', 'addon_purchased'))
    ON CONFLICT (kind, dedupe_key) DO NOTHING;
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER trial_claims_operator_notice AFTER INSERT ON trial_claims
  FOR EACH ROW EXECUTE FUNCTION queue_operator_stream_notice();
CREATE TRIGGER promo_redemptions_operator_notice AFTER INSERT ON promo_redemptions
  FOR EACH ROW EXECUTE FUNCTION queue_operator_stream_notice();
CREATE TRIGGER referral_rewards_operator_notice AFTER INSERT ON referral_rewards
  FOR EACH ROW EXECUTE FUNCTION queue_operator_stream_notice();
CREATE TRIGGER outbox_events_operator_notice AFTER INSERT ON outbox_events
  FOR EACH ROW EXECUTE FUNCTION queue_operator_stream_notice();

-- Application errors, grouped by where they happened and what they said, so
-- the same failure repeating is one row with a count rather than a flood.
-- The message is the error as the process logged it, truncated; it is shown
-- to operators holding system.read and never sent to the operator group,
-- which only learns the process, the fingerprint, and the count.
CREATE TABLE app_errors (
  id uuid DEFAULT gen_random_uuid() NOT NULL,
  fingerprint text NOT NULL,
  process text NOT NULL,
  message text NOT NULL,
  source text NOT NULL DEFAULT '',
  occurrences bigint NOT NULL DEFAULT 1,
  first_seen_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz,
  CONSTRAINT app_errors_pkey PRIMARY KEY (id),
  CONSTRAINT app_errors_fingerprint_key UNIQUE (fingerprint),
  CONSTRAINT app_errors_process_known CHECK (process = ANY (ARRAY['api', 'bot', 'worker']::text[])),
  CONSTRAINT app_errors_message_bounded CHECK (length(message) <= 1000),
  CONSTRAINT app_errors_occurrences_positive CHECK (occurrences > 0)
);
CREATE INDEX app_errors_last_seen_idx ON app_errors (last_seen_at DESC);
