-- Self-employed (НПД) receipts through «Мой налог», as Bedolaga's NaloGO
-- integration issues them: one receipt per settled ruble payment, queued,
-- retried, deduplicated by order, and announced in their own operator topic.
CREATE TABLE self_employed_settings (
    singleton boolean PRIMARY KEY DEFAULT true,
    enabled boolean NOT NULL DEFAULT false,
    -- Receipts are issued only for payments settled after this moment, so
    -- switching the integration on never files last month's income.
    enabled_at timestamptz,
    inn text NOT NULL DEFAULT '',
    password_ciphertext bytea,
    token_ciphertext bytea,
    device_id text NOT NULL DEFAULT '',
    service_name text NOT NULL DEFAULT 'Услуги VPN',
    -- Payment providers whose payments need a receipt. Empty means every
    -- provider; a provider that issues its own fiscal receipt is left out.
    providers text[] NOT NULL DEFAULT '{}',
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by uuid,
    CONSTRAINT self_employed_settings_singleton CHECK (singleton),
    CONSTRAINT self_employed_settings_inn_format CHECK (inn = '' OR inn ~ '^[0-9]{12}$'),
    CONSTRAINT self_employed_settings_service_name_length CHECK (char_length(service_name) BETWEEN 1 AND 128),
    CONSTRAINT self_employed_settings_enabled_complete CHECK (NOT enabled OR (enabled_at IS NOT NULL AND inn <> '' AND password_ciphertext IS NOT NULL))
);

INSERT INTO self_employed_settings DEFAULT VALUES;

CREATE TABLE self_employed_receipts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id uuid NOT NULL REFERENCES orders (id),
    amount_minor bigint NOT NULL,
    service_name text NOT NULL,
    operation_time timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    receipt_uuid text,
    receipt_url text,
    last_error_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    issued_at timestamptz,
    cancelled_at timestamptz,
    CONSTRAINT self_employed_receipts_amount_positive CHECK (amount_minor > 0),
    CONSTRAINT self_employed_receipts_status_known CHECK (status IN ('pending', 'uncertain', 'issued', 'failed', 'cancelled')),
    CONSTRAINT self_employed_receipts_attempts_nonnegative CHECK (attempts >= 0),
    CONSTRAINT self_employed_receipts_issued_complete CHECK (
        (status IN ('issued', 'cancelled')) = (receipt_uuid IS NOT NULL AND issued_at IS NOT NULL)
    ),
    CONSTRAINT self_employed_receipts_cancelled_complete CHECK ((status = 'cancelled') = (cancelled_at IS NOT NULL)),
    -- The dedupe: one receipt per order, ever.
    CONSTRAINT self_employed_receipts_order_unique UNIQUE (order_id)
);

CREATE INDEX self_employed_receipts_due_idx ON self_employed_receipts (next_attempt_at)
    WHERE status IN ('pending', 'uncertain');

-- The operator topic receipts are announced in. The lists repeat every kind
-- already allowed and add 'receipt'.
ALTER TABLE operator_topics DROP CONSTRAINT operator_topics_kind_known;
ALTER TABLE operator_topics ADD CONSTRAINT operator_topics_kind_known CHECK (kind IN (
    'purchase', 'renewal', 'topup', 'refund', 'fulfillment_failure', 'incident', 'backup', 'security',
    'anomaly', 'campaign_test', 'notice_test', 'support', 'trial', 'addon', 'promo', 'partner', 'error',
    'infrastructure', 'report', 'node', 'monitoring', 'marketing', 'receipt'
));
ALTER TABLE operator_notifications DROP CONSTRAINT operator_notifications_kind_known;
ALTER TABLE operator_notifications ADD CONSTRAINT operator_notifications_kind_known CHECK (kind IN (
    'purchase', 'renewal', 'topup', 'refund', 'fulfillment_failure', 'incident', 'backup', 'security',
    'anomaly', 'support', 'trial', 'addon', 'promo', 'partner', 'error', 'infrastructure', 'report',
    'node', 'monitoring', 'marketing', 'receipt'
));
