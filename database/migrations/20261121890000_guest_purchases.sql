-- Guest purchases (Bedolaga's guest_purchase_service, without landing pages):
-- a visitor with no account pays for a subscription with only an e-mail
-- address, for themselves or as a gift, and receives an activation code by
-- e-mail that a customer redeems after signing in.
--
-- The purchase is a subscription gift order whose sender is one system
-- account, the guest checkout, so payment, refunds, expiry, and the claim all
-- run through the gift machinery that already exists. The account holds no
-- identity and cannot sign in.
INSERT INTO users (id, status, locale) VALUES ('00000000-0000-4000-8000-00000000600e', 'active', 'ru');

CREATE TABLE guest_purchases (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id uuid NOT NULL REFERENCES orders (id),
    gift_id uuid NOT NULL REFERENCES gifts (id),
    -- The payer's and the recipient's addresses, sealed with the data key.
    payer_email_ciphertext bytea NOT NULL,
    recipient_email_ciphertext bytea,
    is_gift boolean NOT NULL DEFAULT false,
    locale text NOT NULL DEFAULT 'ru',
    -- The activation code, sealed, kept only until it has been e-mailed.
    code_ciphertext bytea,
    -- SHA-256 of the token the purchase page polls its status with.
    status_token_hash bytea NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    created_at timestamptz NOT NULL DEFAULT now(),
    mailed_at timestamptz,
    CONSTRAINT guest_purchases_status_known CHECK (status IN ('pending', 'mailed', 'expired')),
    CONSTRAINT guest_purchases_mailed_complete CHECK ((status = 'mailed') = (mailed_at IS NOT NULL)),
    CONSTRAINT guest_purchases_code_released CHECK (status = 'pending' OR code_ciphertext IS NULL),
    CONSTRAINT guest_purchases_token_length CHECK (octet_length(status_token_hash) = 32),
    CONSTRAINT guest_purchases_recipient_only_for_gift CHECK (recipient_email_ciphertext IS NULL OR is_gift),
    CONSTRAINT guest_purchases_order_unique UNIQUE (order_id)
);

CREATE INDEX guest_purchases_pending_idx ON guest_purchases (created_at) WHERE status = 'pending';
