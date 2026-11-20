-- How each customer-facing payment button is presented and to whom.
--
-- A button is one method of one provider: the provider alone (method '') or a
-- method the adapter was configured with (`APP_*_PAYMENT_METHODS`), such as
-- FreeKassa's SBP and card systems shown side by side. A row is optional: a
-- configured method without one is shown with its default name to everyone,
-- exactly as before this table existed.
CREATE TABLE payment_method_settings (
    provider text NOT NULL,
    method text NOT NULL DEFAULT '',
    enabled boolean NOT NULL DEFAULT true,
    display_order integer NOT NULL DEFAULT 0,
    name_ru text NOT NULL DEFAULT '',
    name_en text NOT NULL DEFAULT '',
    description_ru text NOT NULL DEFAULT '',
    description_en text NOT NULL DEFAULT '',
    min_minor bigint,
    max_minor bigint,
    quick_amounts_minor bigint[] NOT NULL DEFAULT '{}',
    -- Empty means everyone. Otherwise the customer must hold an active sign-in
    -- of one of these kinds: telegram, email, vk, oidc.
    login_kinds text[] NOT NULL DEFAULT '{}',
    -- all, first_topup (no top-up paid yet) or repeat_topup (at least one).
    topup_audience text NOT NULL DEFAULT 'all',
    -- Empty means every tier; otherwise loyalty tier codes.
    loyalty_tier_codes text[] NOT NULL DEFAULT '{}',
    -- Open the provider's page at once instead of showing the order screen
    -- with a pay button first.
    open_directly boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by uuid,
    CONSTRAINT payment_method_settings_pkey PRIMARY KEY (provider, method),
    CONSTRAINT payment_method_settings_method_format CHECK (method ~ '^[a-z0-9_]{0,10}$'),
    CONSTRAINT payment_method_settings_name_length CHECK (char_length(name_ru) <= 64 AND char_length(name_en) <= 64),
    CONSTRAINT payment_method_settings_description_length CHECK (char_length(description_ru) <= 240 AND char_length(description_en) <= 240),
    CONSTRAINT payment_method_settings_bounds CHECK (
        (min_minor IS NULL OR min_minor > 0) AND (max_minor IS NULL OR max_minor > 0)
        AND (min_minor IS NULL OR max_minor IS NULL OR min_minor <= max_minor)
    ),
    CONSTRAINT payment_method_settings_quick_amounts CHECK (cardinality(quick_amounts_minor) <= 8 AND 0 < ALL (quick_amounts_minor)),
    CONSTRAINT payment_method_settings_login_kinds CHECK (login_kinds <@ ARRAY['telegram', 'email', 'vk', 'oidc']::text[]),
    CONSTRAINT payment_method_settings_topup_audience CHECK (topup_audience IN ('all', 'first_topup', 'repeat_topup'))
);

-- The method a payment was opened with, so a payment page, a receipt, and a
-- later reconciliation name the same button the customer pressed.
ALTER TABLE payment_intents ADD COLUMN method text NOT NULL DEFAULT '';

-- The method a checkout chose, beside its provider.
ALTER TABLE bot_checkout_sessions ADD COLUMN payment_method text NOT NULL DEFAULT '';
