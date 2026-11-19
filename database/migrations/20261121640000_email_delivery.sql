-- Durable outgoing e-mail: receipts, broadcasts, personal offers, guest
-- purchases, and withdrawal updates are queued here in the transaction that
-- decides to send them and delivered by the worker with retries.
--
-- The rendered message (recipient, subject, both bodies, headers) is stored
-- sealed with the installation's data key, so a database read never yields an
-- address or a sign-in link in clear. dedupe_key is what makes enqueueing
-- idempotent: a receipt is keyed by its order, a broadcast by campaign and
-- customer.
CREATE TABLE email_outbox (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid REFERENCES users (id) ON DELETE CASCADE,
    kind text NOT NULL,
    class text NOT NULL,
    dedupe_key text NOT NULL,
    payload_ciphertext bytea NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error_code text,
    campaign_id uuid REFERENCES campaigns (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    sent_at timestamptz,
    CONSTRAINT email_outbox_kind_known CHECK (kind IN (
        'receipt', 'campaign', 'offer', 'guest_purchase', 'guest_gift', 'withdrawal', 'test'
    )),
    CONSTRAINT email_outbox_class_known CHECK (class IN ('transactional', 'marketing')),
    CONSTRAINT email_outbox_status_known CHECK (status IN ('pending', 'sent', 'failed')),
    CONSTRAINT email_outbox_attempts_nonnegative CHECK (attempts >= 0),
    CONSTRAINT email_outbox_dedupe_key_length CHECK (char_length(dedupe_key) BETWEEN 1 AND 200),
    CONSTRAINT email_outbox_sent_complete CHECK ((status = 'sent') = (sent_at IS NOT NULL)),
    CONSTRAINT email_outbox_dedupe_key_unique UNIQUE (dedupe_key)
);

CREATE INDEX email_outbox_due_idx ON email_outbox (next_attempt_at) WHERE status = 'pending';
CREATE INDEX email_outbox_marketing_sent_idx ON email_outbox (user_id, sent_at)
    WHERE class = 'marketing' AND status = 'sent';
CREATE INDEX email_outbox_created_idx ON email_outbox (created_at);

-- A broadcast goes out by Telegram or by e-mail. The bot reads only Telegram
-- campaigns and the worker's mail pass only e-mail ones, so one recipient row
-- is never claimed by both.
ALTER TABLE campaigns ADD COLUMN channel text NOT NULL DEFAULT 'telegram';
ALTER TABLE campaigns ADD CONSTRAINT campaigns_channel_known CHECK (channel IN ('telegram', 'email'));

-- An e-mail campaign recipient with no verified address is recorded, not dropped.
ALTER TABLE campaign_recipients DROP CONSTRAINT campaign_recipients_suppression_reason_check;
ALTER TABLE campaign_recipients ADD CONSTRAINT campaign_recipients_suppression_reason_check CHECK (
    suppression_reason IS NULL OR suppression_reason IN (
        'no_consent', 'suppressed', 'frequency_cap', 'quiet_hours', 'delivery_blocked', 'no_telegram', 'no_email'
    )
);

-- A personal offer may also be e-mailed, through the same consent and
-- frequency policy as a broadcast.
ALTER TABLE personal_offers ADD COLUMN notify_email boolean NOT NULL DEFAULT false;
