-- Remnawave can push its own events — a user's first connection, a traffic
-- threshold, a device added, a node going down — instead of Omniflow finding
-- out on its next fifteen-minute pass, or never. Each verified delivery is
-- recorded here with an allowlisted summary, never the raw body: the panel's
-- user object carries the access link and the device a hardware ID.
--
-- A row moves from pending to delivered, ignored, or failed exactly once, when
-- the bot has acted on it. That is the documented lifecycle; nothing else
-- about a row changes.
CREATE TABLE remnawave_webhook_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event text NOT NULL,
    body_sha256 bytea NOT NULL,
    remnawave_user_id bigint,
    details jsonb DEFAULT '{}'::jsonb NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    processed_at timestamp with time zone,
    CONSTRAINT remnawave_webhook_events_event_shape CHECK (char_length(event) BETWEEN 1 AND 100),
    CONSTRAINT remnawave_webhook_events_status_known CHECK (status = ANY (ARRAY['pending'::text, 'delivered'::text, 'ignored'::text, 'failed'::text])),
    CONSTRAINT remnawave_webhook_events_processed CHECK ((status = 'pending'::text) = (processed_at IS NULL))
);

COMMENT ON TABLE remnawave_webhook_events IS 'Verified Remnawave webhook deliveries, summarised to an allowlist of fields. The raw body is not kept.';

-- A panel that retries a delivery sends the same bytes; the digest makes the
-- retry a no-op rather than a second notification.
CREATE UNIQUE INDEX remnawave_webhook_events_body_idx ON remnawave_webhook_events (body_sha256);
CREATE INDEX remnawave_webhook_events_pending_idx ON remnawave_webhook_events (id) WHERE status = 'pending'::text;

-- The customer notices the panel's events produce, and the reminder
-- constructor's own kind.
ALTER TABLE notification_deliveries DROP CONSTRAINT notification_deliveries_kind_known;
ALTER TABLE notification_deliveries ADD CONSTRAINT notification_deliveries_kind_known CHECK (kind = ANY (ARRAY[
    'expiry'::text, 'traffic'::text, 'renewal'::text, 'grace'::text, 'recovery'::text, 'payment'::text,
    'fulfillment'::text, 'support'::text, 'news'::text, 'announcement'::text, 'incident'::text,
    'maintenance'::text, 'marketing'::text, 'referral'::text, 'trial'::text, 'test'::text,
    'connection'::text, 'device'::text, 'access'::text, 'reminder'::text
]));

-- Operators hear about the panel's own infrastructure (nodes down and back,
-- the panel restarting) and about the monitoring passes (devices over their
-- limit, unusual traffic) in topics of their own.
ALTER TABLE operator_notifications DROP CONSTRAINT operator_notifications_kind_known;
ALTER TABLE operator_notifications ADD CONSTRAINT operator_notifications_kind_known CHECK (kind = ANY (ARRAY[
    'purchase'::text, 'renewal'::text, 'topup'::text, 'refund'::text, 'fulfillment_failure'::text,
    'incident'::text, 'backup'::text, 'security'::text, 'anomaly'::text, 'support'::text,
    'node'::text, 'monitoring'::text
]));

ALTER TABLE operator_topics DROP CONSTRAINT operator_topics_kind_known;
ALTER TABLE operator_topics ADD CONSTRAINT operator_topics_kind_known CHECK (kind = ANY (ARRAY[
    'purchase'::text, 'renewal'::text, 'topup'::text, 'refund'::text, 'fulfillment_failure'::text,
    'incident'::text, 'backup'::text, 'security'::text, 'anomaly'::text, 'campaign_test'::text,
    'notice_test'::text, 'support'::text, 'node'::text, 'monitoring'::text
]));
