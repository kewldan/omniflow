-- Omniflow v0.7 admin operations and commerce.
--
-- Adds the records the operator panel needs to run day-to-day business:
-- anomaly detection and review, an external blocklist with human adjudication,
-- personal offers, wider promotion reward types with explicit stacking rules,
-- recurring payments backed by provider-held payment tokens, gifts, a
-- provider-neutral digital-goods shop, bulk operator actions, and the commerce
-- settings that until now lived only in the environment.
--
-- Three rules from the earlier versions continue to hold here.
--
-- Financial, audit, provider-event, delivery-attempt, and fulfillment-history
-- rows are append-only; a table that carries a documented lifecycle says so on
-- the table itself.
--
-- Nothing in this migration lets an automated signal punish a customer. Every
-- adverse decision — a block, a revoked gift, a dismissed anomaly — records the
-- operator who made it and the reason they gave.
--
-- Money is always an integer in the currency's minor unit, and a row that
-- carries an amount carries the ISO currency beside it.

-- ---------------------------------------------------------------------------
-- Commerce settings
-- ---------------------------------------------------------------------------

-- Wallet top-up policy and subscription concurrency were environment variables
-- in v0.5 because there was no panel to edit them. They become rows here.
--
-- The application seeds this row from the environment the first time it reads
-- it and never writes it again, so an installation upgrading from v0.5 keeps
-- the limits its operator already configured. After that first seed the row is
-- authoritative and the panel is the only thing that changes it; the
-- environment variables stay readable so a restore into a fresh database
-- reproduces the same starting point.
CREATE TABLE commerce_settings (
  singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),

  -- Wallet top-up.
  topup_enabled boolean NOT NULL DEFAULT true,
  topup_currency text NOT NULL DEFAULT 'RUB' CHECK (topup_currency ~ '^[A-Z]{3}$'),
  -- Offered as buttons; free entry stays available regardless of this list.
  topup_presets_minor bigint[] NOT NULL DEFAULT '{}',
  topup_minimum_minor bigint NOT NULL DEFAULT 10000 CHECK (topup_minimum_minor > 0),
  topup_maximum_minor bigint NOT NULL DEFAULT 5000000 CHECK (topup_maximum_minor > 0),
  topup_window_seconds bigint NOT NULL DEFAULT 86400 CHECK (topup_window_seconds > 0),
  topup_window_limit_minor bigint NOT NULL DEFAULT 10000000 CHECK (topup_window_limit_minor >= 0),

  -- Concurrent subscriptions. A plan may narrow this further through
  -- `plans.max_concurrent_per_customer`; neither can widen the other.
  multi_subscription_enabled boolean NOT NULL DEFAULT false,
  max_subscriptions_per_customer integer NOT NULL DEFAULT 3
    CHECK (max_subscriptions_per_customer > 0),

  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid REFERENCES admin_users(id),

  CONSTRAINT commerce_settings_topup_range CHECK (topup_minimum_minor <= topup_maximum_minor)
);

-- ---------------------------------------------------------------------------
-- Payment provider configuration
-- ---------------------------------------------------------------------------

-- One row per provider per merchant. `merchant_id` is the empty string for an
-- installation with a single merchant account, which is every installation
-- until an operator adds a second one; it is part of the key rather than a
-- nullable column so the "default merchant" case needs no null handling
-- anywhere that joins to it.
--
-- Recurring support has two gates. The adapter declares whether it can bind a
-- payment method at all (`payments.Capabilities.Recurring`, compiled in), and
-- this row records whether the operator's own merchant account has been granted
-- that ability — several acquirers enable card binding per merchant rather than
-- per integration. The operator switch can only narrow what the adapter
-- declares, never widen it, and `recurring_test_status` records whether a real
-- capability test has actually been run.
--
-- Secrets are sealed with APP_DATA_ENCRYPTION_KEY exactly like every other
-- ciphertext column in the schema, are never returned by the API after they are
-- written, and are excluded from diagnostics.
--
-- Documented mutable lifecycle: every column except `provider`, `merchant_id`,
-- and `created_at`.
CREATE TABLE payment_provider_settings (
  provider text NOT NULL CHECK (
    provider IN ('telegram_stars', 'cryptobot', 'yookassa', 'manual')
  ),
  merchant_id text NOT NULL DEFAULT '' CHECK (char_length(merchant_id) <= 64),
  enabled boolean NOT NULL DEFAULT false,
  display_order integer NOT NULL DEFAULT 0,

  credentials_ciphertext bytea,
  webhook_secret_ciphertext bytea,

  recurring_enabled boolean NOT NULL DEFAULT false,
  recurring_test_status text NOT NULL DEFAULT 'untested'
    CHECK (recurring_test_status IN ('untested', 'passed', 'failed')),
  recurring_tested_at timestamptz,

  connection_status text NOT NULL DEFAULT 'unknown'
    CHECK (connection_status IN ('unknown', 'healthy', 'failing')),
  connection_checked_at timestamptz,
  connection_error_code text,

  webhook_status text NOT NULL DEFAULT 'unknown'
    CHECK (webhook_status IN ('unknown', 'healthy', 'failing')),
  webhook_last_event_at timestamptz,
  webhook_last_error_code text,

  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid REFERENCES admin_users(id),

  PRIMARY KEY (provider, merchant_id),

  -- Turning recurring on without a passing capability test is exactly the
  -- mistake that produces a customer whose card is never actually charged.
  CONSTRAINT payment_provider_settings_recurring_tested
    CHECK (recurring_enabled = false OR recurring_test_status = 'passed')
);

-- ---------------------------------------------------------------------------
-- Saved payment methods and auto-renew
-- ---------------------------------------------------------------------------

-- A saved method is a reference to something the provider holds, never an
-- instrument. `provider_token` is whatever opaque handle the acquirer issued;
-- `display_label` is the masked description the provider returned for the
-- customer to recognise. Omniflow neither parses nor derives that label, and no
-- column here can hold a card number, expiry, or verification value.
--
-- Documented mutable lifecycle: `display_label`, `status`, `is_default`,
-- `last_used_at`, `updated_at`, and `revoked_at`.
CREATE TABLE payment_methods (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider text NOT NULL CHECK (
    provider IN ('telegram_stars', 'cryptobot', 'yookassa', 'manual')
  ),
  merchant_id text NOT NULL DEFAULT '' CHECK (char_length(merchant_id) <= 64),
  provider_token text NOT NULL CHECK (char_length(provider_token) BETWEEN 1 AND 256),
  display_label text NOT NULL DEFAULT '' CHECK (char_length(display_label) <= 64),

  status text NOT NULL DEFAULT 'active'
    CHECK (status IN ('active', 'expired', 'revoked', 'failed')),
  is_default boolean NOT NULL DEFAULT false,

  -- Auto-renew is opt-in, so the consent that produced this record is part of
  -- the record. A method with no consent cannot be charged.
  consent_at timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  revoked_at timestamptz,

  UNIQUE (provider, merchant_id, provider_token),
  CONSTRAINT payment_methods_revocation_complete
    CHECK ((status = 'revoked') = (revoked_at IS NOT NULL))
);

CREATE UNIQUE INDEX payment_methods_one_default_idx
  ON payment_methods (user_id) WHERE is_default AND status = 'active';
CREATE INDEX payment_methods_customer_idx
  ON payment_methods (user_id, created_at DESC) WHERE status = 'active';

-- Existing auto-renew rows predate consent tracking. They were created by a
-- customer explicitly enabling auto-renew in the bot, so the moment that
-- setting was last written is the consent timestamp; backfilling before the
-- constraint is added is what keeps those rows valid.
ALTER TABLE auto_renew_settings
  ADD COLUMN payment_method_id uuid REFERENCES payment_methods(id),
  ADD COLUMN funding text NOT NULL DEFAULT 'wallet'
    CHECK (funding IN ('wallet', 'saved_method')),
  -- How far ahead of expiry the renewal is attempted. Three days by default,
  -- which leaves room for a failed charge to be retried before access lapses.
  ADD COLUMN lead_time_seconds bigint NOT NULL DEFAULT 259200
    CHECK (lead_time_seconds >= 0),
  ADD COLUMN consent_at timestamptz,
  ADD COLUMN state text NOT NULL DEFAULT 'idle'
    CHECK (state IN ('idle', 'scheduled', 'dunning', 'suspended')),
  ADD COLUMN last_attempt_at timestamptz,
  ADD COLUMN last_failure_code text;

UPDATE auto_renew_settings SET consent_at = updated_at WHERE enabled = true;

ALTER TABLE auto_renew_settings
  ADD CONSTRAINT auto_renew_settings_consent_recorded
    CHECK (enabled = false OR consent_at IS NOT NULL),
  ADD CONSTRAINT auto_renew_settings_saved_method_present
    CHECK (funding <> 'saved_method' OR payment_method_id IS NOT NULL);

-- Failed recurring charges retry on a schedule and then give up, handing the
-- customer back to manual renewal. `cycle_key` identifies the renewal being
-- attempted — one subscription's one period — so attempt numbers cannot
-- collide across cycles and a replayed job finds the row it already wrote.
--
-- Append-only apart from `outcome`, `failure_code`, `notified_at`, and
-- `occurred_at`, which a scheduled attempt fills in when it resolves.
CREATE TABLE dunning_attempts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  subscription_id uuid REFERENCES subscriptions(id),
  cycle_key text NOT NULL CHECK (char_length(cycle_key) BETWEEN 1 AND 128),
  attempt integer NOT NULL CHECK (attempt > 0),
  funding text NOT NULL CHECK (funding IN ('wallet', 'saved_method')),
  payment_method_id uuid REFERENCES payment_methods(id),
  order_id uuid REFERENCES orders(id),

  outcome text NOT NULL DEFAULT 'scheduled'
    CHECK (outcome IN ('scheduled', 'succeeded', 'failed', 'abandoned')),
  failure_code text,
  scheduled_for timestamptz NOT NULL,
  occurred_at timestamptz,
  notified_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),

  UNIQUE (cycle_key, attempt)
);

CREATE INDEX dunning_attempts_due_idx
  ON dunning_attempts (scheduled_for) WHERE outcome = 'scheduled';
CREATE INDEX dunning_attempts_customer_idx ON dunning_attempts (user_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- Promotion reward types, stacking, and personal offers
-- ---------------------------------------------------------------------------

-- v0.3 promotions discounted an order by a percentage or a fixed amount. v0.7
-- adds three rewards that are not discounts: wallet credit, extra subscription
-- days, and a granted trial. They share the promotion, eligibility, redemption
-- limit, and audit machinery rather than growing a parallel one.
--
-- Only the inline column check is dropped. The three table-level checks the
-- original definition carries are left alone: each is written as
-- "kind = X implies Y", so a kind they do not name satisfies them already.
ALTER TABLE promotions
  DROP CONSTRAINT promotions_kind_check;

ALTER TABLE promotions
  ADD CONSTRAINT promotions_kind_known CHECK (
    kind IN ('percent', 'fixed', 'wallet_credit', 'days', 'trial')
  ),
  -- Wallet credit is money and needs a currency for the same reason a fixed
  -- discount does.
  ADD CONSTRAINT promotions_credit_currency_present
    CHECK (kind <> 'wallet_credit' OR currency IS NOT NULL),
  -- Ten years of granted days is far beyond any legitimate promotion and is the
  -- shape a mistyped value takes.
  ADD CONSTRAINT promotions_day_reward_bounded
    CHECK (kind NOT IN ('days', 'trial') OR value <= 3650),

  -- Stacking is off by default: two promotions apply together only when both
  -- say they may. `precedence` orders evaluation, highest first, so the outcome
  -- of applying several is deterministic rather than dependent on the order the
  -- customer happened to enter them.
  ADD COLUMN stackable boolean NOT NULL DEFAULT false,
  ADD COLUMN precedence integer NOT NULL DEFAULT 0;

-- An offer aimed at one named customer. It is a promotion with an audience of
-- one plus its own presentation and validity window, so the redemption path,
-- the limits, and the ledger effects are the ones already tested.
--
-- Documented mutable lifecycle: `status`, `order_id`, `resolved_at`.
CREATE TABLE personal_offers (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  promotion_id uuid NOT NULL REFERENCES promotions(id),
  plan_id uuid REFERENCES plans(id),

  title_ru text NOT NULL CHECK (char_length(title_ru) BETWEEN 1 AND 120),
  title_en text NOT NULL CHECK (char_length(title_en) BETWEEN 1 AND 120),
  terms_ru text NOT NULL DEFAULT '' CHECK (char_length(terms_ru) <= 1000),
  terms_en text NOT NULL DEFAULT '' CHECK (char_length(terms_en) <= 1000),

  status text NOT NULL DEFAULT 'active'
    CHECK (status IN ('active', 'redeemed', 'dismissed', 'expired', 'revoked')),
  starts_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,

  order_id uuid REFERENCES orders(id),
  created_by uuid REFERENCES admin_users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz,

  CONSTRAINT personal_offers_window CHECK (expires_at > starts_at),
  -- Single use: an offer is redeemed exactly once, and the order that redeemed
  -- it is the proof.
  CONSTRAINT personal_offers_redemption_complete
    CHECK ((status = 'redeemed') = (order_id IS NOT NULL))
);

-- One live offer per customer per promotion, so re-running a targeting job
-- cannot stack duplicates in the customer's inbox.
CREATE UNIQUE INDEX personal_offers_one_active_idx
  ON personal_offers (user_id, promotion_id) WHERE status = 'active';
CREATE INDEX personal_offers_customer_idx ON personal_offers (user_id, created_at DESC);
CREATE INDEX personal_offers_expiry_idx ON personal_offers (expires_at) WHERE status = 'active';

-- ---------------------------------------------------------------------------
-- Gifts
-- ---------------------------------------------------------------------------

-- Gift and digital-goods purchases are ordinary orders, so they reuse the whole
-- payment, webhook, reconciliation, and refund pipeline unchanged. This is the
-- same argument that made top-up and add-on purchases orders in v0.5.
ALTER TABLE orders
  DROP CONSTRAINT orders_operation_check;

ALTER TABLE orders
  ADD CONSTRAINT orders_operation_known CHECK (
    operation IN ('purchase', 'upgrade', 'downgrade', 'extension',
                  'topup', 'addon', 'gift', 'goods')
  );

-- A gift is bought by one customer and redeemed by another.
--
-- The order stays in the sender's history and is never copied into the
-- recipient's: `orders.user_id` is the sender, and the entitlement or ledger
-- entry the claim produces carries the recipient. That is what keeps a gift out
-- of the recipient's payment history while still giving them what was bought.
--
-- Only the SHA-256 of the claim code is stored, so a database read never yields
-- a redeemable code. `code_hint` is the last four characters, which is enough
-- for a sender to tell two of their own gifts apart and not enough to redeem
-- one.
--
-- Documented mutable lifecycle: `status`, `recipient_user_id`, `claim_attempts`,
-- `claim_entitlement_id`, `claim_ledger_transaction_id`, `claimed_at`, the
-- revocation columns, and `updated_at`.
CREATE TABLE gifts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  order_id uuid NOT NULL UNIQUE REFERENCES orders(id),
  sender_user_id uuid NOT NULL REFERENCES users(id),

  kind text NOT NULL CHECK (kind IN ('subscription', 'addon', 'wallet_credit')),
  plan_version_id uuid REFERENCES plan_versions(id),
  addon_version_id uuid REFERENCES addon_versions(id),
  credit_minor bigint CHECK (credit_minor IS NULL OR credit_minor > 0),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),

  code_hash bytea NOT NULL UNIQUE CHECK (octet_length(code_hash) = 32),
  code_hint text NOT NULL CHECK (code_hint ~ '^[A-Z0-9]{4}$'),

  -- The intended recipient, when the sender named one. A gift with no named
  -- recipient is claimable by whoever holds the code, which is what makes a
  -- gift link work at all.
  recipient_telegram_id bigint CHECK (recipient_telegram_id IS NULL OR recipient_telegram_id > 0),
  recipient_user_id uuid REFERENCES users(id),
  sender_message text CHECK (sender_message IS NULL OR char_length(sender_message) <= 200),

  status text NOT NULL DEFAULT 'pending' CHECK (
    status IN ('pending', 'deliverable', 'claimed', 'expired', 'revoked', 'refunded')
  ),
  claim_attempts integer NOT NULL DEFAULT 0 CHECK (claim_attempts >= 0),

  claim_entitlement_id uuid REFERENCES entitlements(id),
  claim_ledger_transaction_id uuid REFERENCES ledger_transactions(id),

  expires_at timestamptz NOT NULL,
  claimed_at timestamptz,
  revoked_at timestamptz,
  revoked_by uuid REFERENCES admin_users(id),
  revoke_reason text CHECK (revoke_reason IS NULL OR char_length(revoke_reason) <= 400),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),

  -- Exactly one of the three payloads is present, and it is the one the kind
  -- names.
  CONSTRAINT gifts_subscription_payload
    CHECK ((kind = 'subscription') = (plan_version_id IS NOT NULL)),
  CONSTRAINT gifts_addon_payload
    CHECK ((kind = 'addon') = (addon_version_id IS NOT NULL)),
  CONSTRAINT gifts_credit_payload
    CHECK ((kind = 'wallet_credit') = (credit_minor IS NOT NULL)),

  CONSTRAINT gifts_claim_complete
    CHECK ((status = 'claimed') = (claimed_at IS NOT NULL AND recipient_user_id IS NOT NULL)),
  CONSTRAINT gifts_revocation_complete
    CHECK ((status = 'revoked') = (revoked_at IS NOT NULL AND revoke_reason IS NOT NULL))
);

CREATE INDEX gifts_sender_idx ON gifts (sender_user_id, created_at DESC);
CREATE INDEX gifts_recipient_idx ON gifts (recipient_user_id, claimed_at DESC)
  WHERE recipient_user_id IS NOT NULL;
CREATE INDEX gifts_expiry_idx ON gifts (expires_at) WHERE status = 'deliverable';

-- ---------------------------------------------------------------------------
-- Digital goods
-- ---------------------------------------------------------------------------

-- A digital good is not VPN access. Nothing in this section creates, modifies,
-- or reads a Remnawave entitlement, and a delivery failure here can never
-- disturb a subscription.
--
-- `slug` is a pattern rather than an enumeration because the adapter contract
-- is provider-neutral; `fragment` is the only implementation that ships in
-- v0.7. Credentials are sealed like every other secret in the schema.
--
-- Documented mutable lifecycle: every column except `slug` and `created_at`.
CREATE TABLE goods_providers (
  slug text PRIMARY KEY CHECK (slug ~ '^[a-z][a-z0-9_-]{1,31}$'),
  enabled boolean NOT NULL DEFAULT false,
  credentials_ciphertext bytea,

  -- The provider's own balance, refreshed by the health probe. A shop that
  -- keeps selling after its funding source runs dry produces paid orders that
  -- can only be refunded, so the threshold below is what raises the alert
  -- before that happens.
  balance_minor bigint CHECK (balance_minor IS NULL OR balance_minor >= 0),
  balance_currency text CHECK (balance_currency IS NULL OR balance_currency ~ '^[A-Z]{3}$'),
  low_balance_threshold_minor bigint
    CHECK (low_balance_threshold_minor IS NULL OR low_balance_threshold_minor >= 0),

  -- Spend ceiling over a rolling window, enforced before a delivery is
  -- submitted. Zero disables the ceiling.
  spend_limit_minor bigint NOT NULL DEFAULT 0 CHECK (spend_limit_minor >= 0),
  spend_window_seconds bigint NOT NULL DEFAULT 86400 CHECK (spend_window_seconds > 0),

  status text NOT NULL DEFAULT 'unknown'
    CHECK (status IN ('unknown', 'healthy', 'failing')),
  last_error_code text,
  last_checked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT goods_providers_balance_currency_present
    CHECK ((balance_minor IS NULL) = (balance_currency IS NULL))
);

CREATE TABLE goods_products (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  code text NOT NULL UNIQUE CHECK (code ~ '^[a-z][a-z0-9_-]{1,63}$'),
  provider_slug text NOT NULL REFERENCES goods_providers(slug),
  kind text NOT NULL CHECK (kind IN ('telegram_premium', 'telegram_stars')),

  -- Premium is sold by duration, Stars by quantity. Each kind carries exactly
  -- the one that applies to it.
  duration_months smallint CHECK (duration_months IS NULL OR duration_months IN (3, 6, 12)),
  star_quantity integer CHECK (star_quantity IS NULL OR star_quantity > 0),

  visible boolean NOT NULL DEFAULT false,
  sort_order integer NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  archived_at timestamptz,

  CONSTRAINT goods_products_premium_shape
    CHECK ((kind = 'telegram_premium') = (duration_months IS NOT NULL)),
  CONSTRAINT goods_products_stars_shape
    CHECK ((kind = 'telegram_stars') = (star_quantity IS NOT NULL))
);

CREATE INDEX goods_products_catalog_idx ON goods_products (sort_order, code)
  WHERE visible AND archived_at IS NULL;

CREATE TABLE goods_product_localizations (
  product_id uuid NOT NULL REFERENCES goods_products(id) ON DELETE CASCADE,
  locale text NOT NULL CHECK (locale IN ('ru', 'en')),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
  PRIMARY KEY (product_id, locale)
);

-- What the customer pays is derived from what the provider charges: a markup in
-- basis points, then a rounding rule, both operator-configured. Providers whose
-- rate moves with an exchange or a market price therefore need a quote with an
-- expiry, which `quote_ttl_seconds` sets.
--
-- `fixed_amount_minor` opts out of the derivation entirely for an operator who
-- would rather publish one price and absorb the variance.
CREATE TABLE goods_pricing (
  product_id uuid PRIMARY KEY REFERENCES goods_products(id) ON DELETE CASCADE,
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  markup_bps integer NOT NULL DEFAULT 0 CHECK (markup_bps BETWEEN 0 AND 100000),
  rounding text NOT NULL DEFAULT 'up_minor'
    CHECK (rounding IN ('none', 'up_minor', 'up_unit', 'up_ten_units', 'up_hundred_units')),
  fixed_amount_minor bigint CHECK (fixed_amount_minor IS NULL OR fixed_amount_minor > 0),
  quote_ttl_seconds integer NOT NULL DEFAULT 300 CHECK (quote_ttl_seconds BETWEEN 30 AND 3600),
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid REFERENCES admin_users(id)
);

-- The commerce side of a shop purchase. It hangs off an ordinary order, so the
-- customer's payment, refund, and wallet history need no special case.
--
-- The recipient is a Telegram username and nothing else. No display name, no
-- numeric identifier, no profile data: delivery needs the username and support
-- needs to be able to answer "where did it go", and neither needs more.
--
-- Documented mutable lifecycle: `status` and `updated_at`.
CREATE TABLE goods_orders (
  order_id uuid PRIMARY KEY REFERENCES orders(id),
  user_id uuid NOT NULL REFERENCES users(id),
  product_id uuid NOT NULL REFERENCES goods_products(id),
  quantity integer NOT NULL DEFAULT 1 CHECK (quantity > 0),

  recipient_username text NOT NULL CHECK (recipient_username ~ '^[A-Za-z0-9_]{5,32}$'),
  recipient_is_self boolean NOT NULL DEFAULT false,

  -- Both sides of the quote are kept: what the provider asked and what the
  -- customer was shown. Their difference is the realised margin, and keeping
  -- them separate is what lets the finance view report it without recomputing a
  -- markup that may since have changed.
  quoted_cost_minor bigint NOT NULL CHECK (quoted_cost_minor >= 0),
  quoted_price_minor bigint NOT NULL CHECK (quoted_price_minor >= 0),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  quote_expires_at timestamptz NOT NULL,

  status text NOT NULL DEFAULT 'quoted' CHECK (
    status IN ('quoted', 'paid', 'delivering', 'delivered', 'failed', 'refunded')
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX goods_orders_customer_idx ON goods_orders (user_id, created_at DESC);
CREATE INDEX goods_orders_status_idx ON goods_orders (status, created_at DESC);

-- One delivery per order, enforced by the primary key. That single constraint
-- is what makes double delivery impossible: a replayed job, a duplicated
-- webhook, and a retried worker all find the row that already exists and act on
-- its recorded state instead of submitting a second time.
--
-- Documented mutable lifecycle: `status`, `provider_reference`,
-- `attempt_count`, `next_attempt_at`, `failure_class`, `last_error_code`,
-- `refund_ledger_transaction_id`, `delivered_at`, and `updated_at`.
CREATE TABLE goods_deliveries (
  order_id uuid PRIMARY KEY REFERENCES orders(id),
  provider_slug text NOT NULL REFERENCES goods_providers(slug),
  idempotency_key text NOT NULL UNIQUE,
  provider_reference text,

  status text NOT NULL DEFAULT 'pending' CHECK (
    status IN ('pending', 'submitted', 'delivered', 'failed', 'cancelled')
  ),
  attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),

  -- Classification, not a free-text message: it decides whether the delivery is
  -- retried, handed back to the customer to correct, or refunded.
  failure_class text CHECK (
    failure_class IS NULL
    OR failure_class IN ('retryable', 'permanent', 'recipient_invalid',
                         'provider_balance', 'provider_unavailable')
  ),
  last_error_code text,

  -- Set when a permanent failure has been made good. The wallet credit is an
  -- ordinary ledger transaction, so the refund appears in the customer's wallet
  -- history exactly like any other.
  refund_ledger_transaction_id uuid REFERENCES ledger_transactions(id),

  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  delivered_at timestamptz,

  CONSTRAINT goods_deliveries_delivery_complete
    CHECK ((status = 'delivered') = (delivered_at IS NOT NULL))
);

CREATE INDEX goods_deliveries_due_idx
  ON goods_deliveries (next_attempt_at) WHERE status IN ('pending', 'submitted');

-- Every provider exchange, kept for support and for the failure classification
-- that produced a refund. Append-only, and it never stores a credential or a
-- raw provider payload.
CREATE TABLE goods_delivery_attempts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  order_id uuid NOT NULL REFERENCES goods_deliveries(order_id) ON DELETE CASCADE,
  attempt integer NOT NULL CHECK (attempt > 0),
  outcome text NOT NULL CHECK (outcome IN ('submitted', 'delivered', 'failed')),
  failure_class text,
  error_code text,
  provider_reference text,
  correlation_id text NOT NULL,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (order_id, attempt)
);

-- ---------------------------------------------------------------------------
-- External blocklist
-- ---------------------------------------------------------------------------

-- An operator may subscribe to a list of identifiers to refuse. A match is a
-- signal for a human, never an automatic sanction: the panel shows the evidence
-- and an operator decides, and their decision and reason are what the customer
-- record ends up carrying.
--
-- Documented mutable lifecycle: every column except `id`, `slug`, and
-- `created_at`.
CREATE TABLE blocklist_sources (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z][a-z0-9-]{1,38}[a-z0-9]$'),
  display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 80),
  -- What the entries in this list identify.
  subject_kind text NOT NULL CHECK (subject_kind IN ('telegram_id', 'email', 'username')),
  url text NOT NULL CHECK (url ~ '^https://'),
  auth_header_ciphertext bytea,

  enabled boolean NOT NULL DEFAULT false,
  refresh_interval_seconds bigint NOT NULL DEFAULT 86400
    CHECK (refresh_interval_seconds >= 300),
  entry_count integer NOT NULL DEFAULT 0 CHECK (entry_count >= 0),

  status text NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'healthy', 'failing')),
  last_error_code text,
  last_refresh_at timestamptz,
  next_refresh_at timestamptz NOT NULL DEFAULT now(),

  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid REFERENCES admin_users(id)
);

CREATE INDEX blocklist_sources_due_idx ON blocklist_sources (next_refresh_at) WHERE enabled;

-- Only a digest of the normalised value is stored. Omniflow therefore holds no
-- readable copy of a third party's list, a fingerprint from one installation
-- means nothing in another, and a lookup is still an exact index probe.
CREATE TABLE blocklist_entries (
  source_id uuid NOT NULL REFERENCES blocklist_sources(id) ON DELETE CASCADE,
  value_fingerprint bytea NOT NULL CHECK (octet_length(value_fingerprint) = 32),
  reason_code text CHECK (reason_code IS NULL OR char_length(reason_code) <= 64),
  imported_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (source_id, value_fingerprint)
);

-- A customer identifier that matched a list. `status` is the operator's
-- decision, not the list's opinion.
--
-- Documented mutable lifecycle: `status`, `decision_reason`, `decided_by`,
-- `decided_at`.
CREATE TABLE blocklist_matches (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  source_id uuid NOT NULL REFERENCES blocklist_sources(id) ON DELETE CASCADE,
  subject_kind text NOT NULL CHECK (subject_kind IN ('telegram_id', 'email', 'username')),
  value_fingerprint bytea NOT NULL CHECK (octet_length(value_fingerprint) = 32),

  status text NOT NULL DEFAULT 'open'
    CHECK (status IN ('open', 'allowed', 'blocked', 'appealed')),
  decision_reason text CHECK (decision_reason IS NULL OR char_length(decision_reason) <= 400),
  decided_by uuid REFERENCES admin_users(id),
  detected_at timestamptz NOT NULL DEFAULT now(),
  decided_at timestamptz,

  UNIQUE (user_id, source_id, value_fingerprint),
  CONSTRAINT blocklist_matches_decision_complete
    CHECK ((status IN ('open', 'appealed')) = (decided_at IS NULL))
);

CREATE INDEX blocklist_matches_open_idx ON blocklist_matches (detected_at DESC)
  WHERE status IN ('open', 'appealed');

-- A manual override that survives the next refresh of every list. Without it an
-- operator's "this one is fine" decision would be undone the moment the source
-- re-published the same entry.
CREATE TABLE blocklist_allowlist (
  user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  reason text NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 400),
  added_by uuid REFERENCES admin_users(id),
  created_at timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- Anomaly detection and review
-- ---------------------------------------------------------------------------

-- Thresholds are operator-configured per metric, and a metric with no row is
-- simply not evaluated. Keeping the numbers in a table rather than in code is
-- what lets an installation with unusual traffic stop being alerted about it.
CREATE TABLE anomaly_rules (
  metric text PRIMARY KEY CHECK (
    metric IN ('traffic', 'purchase', 'refund', 'referral')
  ),
  enabled boolean NOT NULL DEFAULT true,
  window_seconds bigint NOT NULL DEFAULT 3600 CHECK (window_seconds >= 300),
  -- In the metric's own unit: bytes for traffic, minor units for purchase and
  -- refund, a count for referral.
  warn_threshold bigint NOT NULL CHECK (warn_threshold > 0),
  alert_threshold bigint NOT NULL CHECK (alert_threshold > 0),
  -- Below this many observations the window is too small to say anything, so
  -- nothing is raised. It is what stops a brand-new installation alerting on
  -- its first three orders.
  minimum_sample integer NOT NULL DEFAULT 3 CHECK (minimum_sample >= 0),
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid REFERENCES admin_users(id),

  CONSTRAINT anomaly_rules_threshold_order CHECK (alert_threshold >= warn_threshold)
);

-- A raised signal. `evidence` carries the aggregate numbers that produced it —
-- counts, sums, and the window — and never a message body, a link, a token, or
-- any other customer content.
--
-- `dedupe_key` is what keeps a condition that persists across several
-- evaluation runs from becoming a new alert every time.
--
-- Documented mutable lifecycle: `status`, `reviewed_by`, `review_reason`,
-- `reviewed_at`.
CREATE TABLE anomaly_signals (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  metric text NOT NULL,
  severity text NOT NULL CHECK (severity IN ('warning', 'alert')),
  subject_type text NOT NULL CHECK (
    subject_type IN ('installation', 'customer', 'plan', 'provider')
  ),
  subject_id text NOT NULL CHECK (char_length(subject_id) BETWEEN 1 AND 128),

  observed bigint NOT NULL,
  threshold bigint NOT NULL,
  sample_size integer NOT NULL DEFAULT 0 CHECK (sample_size >= 0),
  window_started_at timestamptz NOT NULL,
  window_ended_at timestamptz NOT NULL,
  evidence jsonb NOT NULL DEFAULT '{}'::jsonb,

  status text NOT NULL DEFAULT 'open'
    CHECK (status IN ('open', 'acknowledged', 'dismissed')),
  reviewed_by uuid REFERENCES admin_users(id),
  review_reason text CHECK (review_reason IS NULL OR char_length(review_reason) <= 400),
  detected_at timestamptz NOT NULL DEFAULT now(),
  reviewed_at timestamptz,

  dedupe_key text NOT NULL,
  UNIQUE (metric, dedupe_key),
  CONSTRAINT anomaly_signals_window CHECK (window_ended_at > window_started_at),
  CONSTRAINT anomaly_signals_review_complete
    CHECK ((status = 'open') = (reviewed_at IS NULL))
);

CREATE INDEX anomaly_signals_open_idx ON anomaly_signals (detected_at DESC) WHERE status = 'open';
CREATE INDEX anomaly_signals_subject_idx
  ON anomaly_signals (subject_type, subject_id, detected_at DESC);

-- ---------------------------------------------------------------------------
-- Bulk operator actions
-- ---------------------------------------------------------------------------

-- A bulk action is previewed, then applied, and each target records its own
-- outcome. Nothing here applies a change without an operator confirming the
-- preview, and `reason` is mandatory because a hundred-row change with no
-- explanation is unreviewable after the fact.
--
-- Documented mutable lifecycle: the counters, `status`, `updated_at`, and
-- `completed_at`.
CREATE TABLE bulk_operations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  kind text NOT NULL CHECK (
    kind IN ('customer_export', 'subscription_extend', 'subscription_disable',
             'subscription_enable', 'wallet_credit')
  ),
  status text NOT NULL DEFAULT 'previewing' CHECK (
    status IN ('previewing', 'ready', 'running', 'completed', 'failed', 'cancelled')
  ),
  requested_by uuid NOT NULL REFERENCES admin_users(id),
  reason text NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 400),
  parameters jsonb NOT NULL DEFAULT '{}'::jsonb,

  total_count integer NOT NULL DEFAULT 0 CHECK (total_count >= 0),
  succeeded_count integer NOT NULL DEFAULT 0 CHECK (succeeded_count >= 0),
  failed_count integer NOT NULL DEFAULT 0 CHECK (failed_count >= 0),
  skipped_count integer NOT NULL DEFAULT 0 CHECK (skipped_count >= 0),

  idempotency_key text NOT NULL UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz
);

CREATE INDEX bulk_operations_recent_idx ON bulk_operations (created_at DESC);

CREATE TABLE bulk_operation_items (
  operation_id uuid NOT NULL REFERENCES bulk_operations(id) ON DELETE CASCADE,
  position integer NOT NULL CHECK (position >= 0),
  target_type text NOT NULL CHECK (target_type IN ('customer', 'subscription')),
  target_id uuid NOT NULL,
  status text NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'succeeded', 'failed', 'skipped')),
  error_code text,
  processed_at timestamptz,
  PRIMARY KEY (operation_id, position)
);

CREATE INDEX bulk_operation_items_pending_idx
  ON bulk_operation_items (operation_id, position) WHERE status = 'pending';

-- ---------------------------------------------------------------------------
-- Notification and audit vocabulary
-- ---------------------------------------------------------------------------

-- Anomaly alerts need a topic of their own so an operator can mute routine
-- commerce traffic without also muting a fraud signal. Both tables enumerate
-- the known kinds, so the new one has to be admitted in both: widening only the
-- notification table would let a notice be queued and then never bind a topic
-- to deliver it through.
ALTER TABLE operator_topics
  DROP CONSTRAINT operator_topics_kind_known;

ALTER TABLE operator_topics
  ADD CONSTRAINT operator_topics_kind_known CHECK (
    kind IN ('purchase', 'renewal', 'topup', 'refund',
             'fulfillment_failure', 'incident', 'backup', 'security', 'anomaly')
  );

ALTER TABLE operator_notifications
  DROP CONSTRAINT operator_notifications_kind_known;

ALTER TABLE operator_notifications
  ADD CONSTRAINT operator_notifications_kind_known CHECK (
    kind IN ('purchase', 'renewal', 'topup', 'refund',
             'fulfillment_failure', 'incident', 'backup', 'security', 'anomaly')
  );

-- Blocklist adjudication and anomaly review are neither ordinary customer
-- administration nor system maintenance, and collapsing them into either would
-- make a risk review impossible to filter for.
ALTER TABLE audit_events
  DROP CONSTRAINT audit_events_category_known;

ALTER TABLE audit_events
  ADD CONSTRAINT audit_events_category_known CHECK (
    category IN (
      'authentication', 'authorization', 'configuration',
      'customer', 'financial', 'support', 'marketing', 'system', 'risk'
    )
  );

-- ---------------------------------------------------------------------------
-- Bot flows
-- ---------------------------------------------------------------------------

-- The gift and shop flows each need a step where the customer types something
-- the keyboard cannot express: a recipient username, a gift note, a claim code.
ALTER TABLE bot_sessions
  DROP CONSTRAINT bot_sessions_state_check;

ALTER TABLE bot_sessions
  ADD CONSTRAINT bot_sessions_state_check CHECK (
    state IN ('support_message', 'support_reply', 'promo_code',
              'topup_amount', 'subscription_label',
              'gift_recipient', 'gift_message', 'gift_claim', 'goods_recipient')
  );

-- ---------------------------------------------------------------------------
-- Customer search
-- ---------------------------------------------------------------------------

-- The panel searches customers by the identifiers an operator can safely be
-- given: an Omniflow identifier, a Remnawave username, a subscription label.
-- These indexes are what keep that search from scanning the customer table on
-- an installation with tens of thousands of rows.
CREATE INDEX subscriptions_username_idx
  ON subscriptions (remnawave_username) WHERE remnawave_username IS NOT NULL;
CREATE INDEX users_status_created_idx ON users (status, created_at DESC);
CREATE INDEX orders_customer_recent_idx ON orders (user_id, created_at DESC);
CREATE INDEX payment_intents_order_idx ON payment_intents (order_id, created_at DESC);
CREATE INDEX refunds_intent_idx ON refunds (payment_intent_id, created_at DESC);
