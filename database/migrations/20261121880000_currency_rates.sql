-- Multi-currency, as Bedolaga's TELEGRAM_STARS_RATE_RUB and its currency
-- converter provide: Telegram Stars can pay a ruble-priced order at a rate,
-- and the USD/RUB rate is read automatically with a fallback.
CREATE TABLE currency_settings (
    singleton boolean PRIMARY KEY DEFAULT true,
    -- Stars for orders priced in stars_currency.
    stars_enabled boolean NOT NULL DEFAULT false,
    stars_currency text NOT NULL DEFAULT 'RUB',
    -- manual: stars_rate_minor minor units per Star. auto: the USD/RUB rate
    -- times stars_usd_micros (USD millionths per Star; Telegram's own price
    -- is about 0.013 USD).
    stars_rate_mode text NOT NULL DEFAULT 'manual',
    stars_rate_minor bigint NOT NULL DEFAULT 180,
    stars_usd_micros bigint NOT NULL DEFAULT 13000,
    -- auto: the Central Bank of Russia with a second source as fallback.
    -- manual: usd_rub_manual_minor. In auto mode the manual value is the last
    -- resort when no source has answered for two days.
    usd_rub_mode text NOT NULL DEFAULT 'auto',
    usd_rub_manual_minor bigint,
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by uuid,
    CONSTRAINT currency_settings_singleton CHECK (singleton),
    CONSTRAINT currency_settings_stars_currency_iso CHECK (stars_currency ~ '^[A-Z]{3}$' AND stars_currency <> 'XTR'),
    CONSTRAINT currency_settings_stars_mode_known CHECK (stars_rate_mode IN ('manual', 'auto')),
    CONSTRAINT currency_settings_stars_rate_positive CHECK (stars_rate_minor > 0 AND stars_usd_micros > 0),
    CONSTRAINT currency_settings_usd_mode_known CHECK (usd_rub_mode IN ('auto', 'manual')),
    CONSTRAINT currency_settings_usd_manual_positive CHECK (usd_rub_manual_minor IS NULL OR usd_rub_manual_minor > 0),
    CONSTRAINT currency_settings_usd_manual_required CHECK (usd_rub_mode = 'auto' OR usd_rub_manual_minor IS NOT NULL)
);

INSERT INTO currency_settings DEFAULT VALUES;

-- The last good reading of each automatic rate, so a restart during an
-- outage of every source still has a recent rate to fall back on.
CREATE TABLE exchange_rate_observations (
    pair text PRIMARY KEY,
    minor bigint NOT NULL,
    source text NOT NULL,
    observed_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT exchange_rate_observations_pair_format CHECK (pair ~ '^[A-Z]{3}/[A-Z]{3}$'),
    CONSTRAINT exchange_rate_observations_minor_positive CHECK (minor > 0)
);

-- The Stars price of one ruble order, fixed the first time it is asked for so
-- the invoice, Telegram's pre-checkout, and settlement agree even if the rate
-- changes in between.
CREATE TABLE stars_quotes (
    order_id uuid PRIMARY KEY REFERENCES orders (id) ON DELETE CASCADE,
    amount_minor bigint NOT NULL,
    currency text NOT NULL,
    stars bigint NOT NULL,
    rate_minor bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT stars_quotes_amount_positive CHECK (amount_minor > 0),
    CONSTRAINT stars_quotes_stars_positive CHECK (stars > 0),
    CONSTRAINT stars_quotes_rate_positive CHECK (rate_minor > 0)
);
