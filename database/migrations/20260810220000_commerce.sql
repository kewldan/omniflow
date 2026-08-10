-- Omniflow v0.3 customer, catalog, commerce, ledger, and entitlement model.
-- Financial, consent, audit, provider-event, and fulfillment-history rows are append-only.

ALTER TABLE users
  ADD COLUMN timezone text NOT NULL DEFAULT 'UTC',
  ADD COLUMN suspended_at timestamptz,
  ADD COLUMN deleted_at timestamptz,
  ADD COLUMN anonymized_at timestamptz,
  ADD COLUMN retention_until timestamptz;

ALTER TABLE identities
  ADD COLUMN status text NOT NULL DEFAULT 'active' CHECK (status IN ('pending', 'active', 'revoked')),
  ADD COLUMN revoked_at timestamptz,
  ADD COLUMN metadata jsonb NOT NULL DEFAULT '{}'::jsonb;

CREATE UNIQUE INDEX identities_one_active_provider_per_user_idx
  ON identities (user_id, provider) WHERE status = 'active';

CREATE TABLE contact_channels (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id),
  kind text NOT NULL CHECK (kind IN ('email', 'phone', 'telegram')),
  value_ciphertext bytea,
  value_fingerprint bytea NOT NULL,
  verified_at timestamptz,
  transactional_enabled boolean NOT NULL DEFAULT true,
  marketing_enabled boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  revoked_at timestamptz,
  UNIQUE (kind, value_fingerprint)
);

CREATE INDEX contact_channels_user_idx ON contact_channels (user_id, created_at DESC);

CREATE TABLE consent_records (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id),
  purpose text NOT NULL CHECK (purpose IN ('terms', 'privacy', 'marketing', 'profiling')),
  granted boolean NOT NULL,
  policy_version text NOT NULL,
  source text NOT NULL,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  request_id text
);

CREATE INDEX consent_records_user_purpose_idx
  ON consent_records (user_id, purpose, occurred_at DESC);

CREATE TABLE customer_lifecycle_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id),
  action text NOT NULL CHECK (action IN ('suspended', 'restored', 'deletion_requested', 'deleted', 'anonymized')),
  reason text NOT NULL,
  actor_type text NOT NULL CHECK (actor_type IN ('customer', 'operator', 'system')),
  actor_id text,
  request_id text,
  occurred_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE customer_imports (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  source text NOT NULL CHECK (source = 'remnawave'),
  status text NOT NULL DEFAULT 'previewing' CHECK (status IN ('previewing', 'ready', 'applying', 'completed', 'failed', 'cancelled')),
  cursor text,
  total_count integer NOT NULL DEFAULT 0 CHECK (total_count >= 0),
  valid_count integer NOT NULL DEFAULT 0 CHECK (valid_count >= 0),
  conflict_count integer NOT NULL DEFAULT 0 CHECK (conflict_count >= 0),
  invalid_count integer NOT NULL DEFAULT 0 CHECK (invalid_count >= 0),
  error_summary jsonb NOT NULL DEFAULT '{}'::jsonb,
  started_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz
);

CREATE TABLE customer_import_items (
  import_id uuid NOT NULL REFERENCES customer_imports(id) ON DELETE CASCADE,
  source_id text NOT NULL,
  status text NOT NULL CHECK (status IN ('valid', 'conflict', 'invalid', 'applied', 'skipped')),
  fingerprint bytea NOT NULL,
  staged_data jsonb NOT NULL,
  validation_errors jsonb NOT NULL DEFAULT '[]'::jsonb,
  user_id uuid REFERENCES users(id),
  applied_at timestamptz,
  PRIMARY KEY (import_id, source_id)
);

CREATE TABLE plans (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  code text NOT NULL UNIQUE CHECK (code ~ '^[a-z][a-z0-9_-]{1,63}$'),
  kind text NOT NULL CHECK (kind IN ('trial', 'one_time', 'recurring', 'free', 'manual')),
  visible boolean NOT NULL DEFAULT false,
  sort_order integer NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  archived_at timestamptz
);

CREATE TABLE plan_localizations (
  plan_id uuid NOT NULL REFERENCES plans(id),
  locale text NOT NULL CHECK (locale IN ('ru', 'en')),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
  PRIMARY KEY (plan_id, locale)
);

CREATE TABLE plan_versions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  plan_id uuid NOT NULL REFERENCES plans(id),
  version integer NOT NULL CHECK (version > 0),
  billing_period text NOT NULL CHECK (billing_period IN ('none', 'day', 'week', 'month', 'quarter', 'half_year', 'year', 'custom')),
  duration_seconds bigint NOT NULL CHECK (duration_seconds > 0),
  traffic_allowance_bytes bigint CHECK (traffic_allowance_bytes IS NULL OR traffic_allowance_bytes >= 0),
  device_limit integer CHECK (device_limit IS NULL OR device_limit >= 0),
  remnawave_squad_ids uuid[] NOT NULL DEFAULT '{}',
  upgrade_policy text NOT NULL DEFAULT 'extend' CHECK (upgrade_policy IN ('forbid', 'replace', 'extend')),
  downgrade_policy text NOT NULL DEFAULT 'at_expiry' CHECK (downgrade_policy IN ('forbid', 'immediate', 'at_expiry')),
  cancellation_policy text NOT NULL DEFAULT 'at_expiry' CHECK (cancellation_policy IN ('forbid', 'immediate', 'at_expiry')),
  recurring_capable boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  retired_at timestamptz,
  UNIQUE (plan_id, version)
);

CREATE TABLE plan_prices (
  plan_version_id uuid NOT NULL REFERENCES plan_versions(id),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  amount_minor bigint NOT NULL CHECK (amount_minor >= 0),
  PRIMARY KEY (plan_version_id, currency)
);

CREATE TABLE promotions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  code text NOT NULL UNIQUE CHECK (code ~ '^[a-z][a-z0-9_-]{1,63}$'),
  kind text NOT NULL CHECK (kind IN ('percent', 'fixed')),
  value bigint NOT NULL CHECK (value > 0),
  currency text CHECK (currency IS NULL OR currency ~ '^[A-Z]{3}$'),
  starts_at timestamptz,
  ends_at timestamptz,
  redemption_limit integer CHECK (redemption_limit IS NULL OR redemption_limit > 0),
  per_customer_limit integer NOT NULL DEFAULT 1 CHECK (per_customer_limit > 0),
  eligibility jsonb NOT NULL DEFAULT '{}'::jsonb,
  active boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  CHECK (ends_at IS NULL OR starts_at IS NULL OR ends_at > starts_at),
  CHECK ((kind = 'fixed' AND currency IS NOT NULL) OR kind <> 'fixed'),
  CHECK ((kind = 'percent' AND value <= 10000) OR kind <> 'percent')
);

CREATE TABLE promotion_plans (
  promotion_id uuid NOT NULL REFERENCES promotions(id) ON DELETE CASCADE,
  plan_id uuid NOT NULL REFERENCES plans(id),
  PRIMARY KEY (promotion_id, plan_id)
);

CREATE TABLE promo_codes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  promotion_id uuid NOT NULL REFERENCES promotions(id),
  normalized_code text NOT NULL UNIQUE CHECK (normalized_code ~ '^[A-Z0-9_-]{3,64}$'),
  redemption_limit integer CHECK (redemption_limit IS NULL OR redemption_limit > 0),
  active boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE orders (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id),
  state text NOT NULL DEFAULT 'draft' CHECK (state IN ('draft', 'pending', 'paid', 'fulfilled', 'cancelled', 'expired', 'partially_refunded', 'refunded')),
  operation text NOT NULL CHECK (operation IN ('purchase', 'upgrade', 'downgrade', 'extension')),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  subtotal_minor bigint NOT NULL CHECK (subtotal_minor >= 0),
  discount_minor bigint NOT NULL DEFAULT 0 CHECK (discount_minor >= 0),
  wallet_minor bigint NOT NULL DEFAULT 0 CHECK (wallet_minor >= 0),
  external_minor bigint NOT NULL CHECK (external_minor >= 0),
  paid_minor bigint NOT NULL DEFAULT 0 CHECK (paid_minor >= 0),
  refunded_minor bigint NOT NULL DEFAULT 0 CHECK (refunded_minor >= 0),
  idempotency_key text NOT NULL,
  expires_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (discount_minor <= subtotal_minor),
  CHECK (wallet_minor + external_minor = subtotal_minor - discount_minor),
  CHECK (refunded_minor <= paid_minor),
  UNIQUE (user_id, idempotency_key)
);

CREATE TABLE order_lines (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  order_id uuid NOT NULL REFERENCES orders(id),
  plan_id uuid NOT NULL REFERENCES plans(id),
  plan_version_id uuid NOT NULL REFERENCES plan_versions(id),
  quantity integer NOT NULL DEFAULT 1 CHECK (quantity > 0),
  unit_amount_minor bigint NOT NULL CHECK (unit_amount_minor >= 0),
  snapshot jsonb NOT NULL,
  UNIQUE (order_id, plan_version_id)
);

CREATE TABLE order_mutations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  order_id uuid NOT NULL REFERENCES orders(id),
  action text NOT NULL CHECK (action IN ('cancel', 'expire')),
  idempotency_key text NOT NULL,
  reason text,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (order_id, action, idempotency_key)
);

CREATE TABLE promo_redemptions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  promo_code_id uuid NOT NULL REFERENCES promo_codes(id),
  promotion_id uuid NOT NULL REFERENCES promotions(id),
  user_id uuid NOT NULL REFERENCES users(id),
  order_id uuid NOT NULL UNIQUE REFERENCES orders(id),
  discount_minor bigint NOT NULL CHECK (discount_minor >= 0),
  redeemed_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX promo_redemptions_limits_idx ON promo_redemptions (promo_code_id, user_id);

CREATE TABLE payment_intents (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  order_id uuid NOT NULL REFERENCES orders(id),
  provider text NOT NULL CHECK (provider IN ('telegram_stars', 'cryptobot', 'yookassa', 'manual')),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'requires_action', 'processing', 'succeeded', 'failed', 'cancelled', 'expired')),
  amount_minor bigint NOT NULL CHECK (amount_minor >= 0),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  provider_reference text,
  checkout_url text,
  idempotency_key text NOT NULL,
  capabilities jsonb NOT NULL DEFAULT '{}'::jsonb,
  receipt_metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (provider, idempotency_key)
);

CREATE UNIQUE INDEX payment_intents_provider_reference_idx
  ON payment_intents (provider, provider_reference) WHERE provider_reference IS NOT NULL;

CREATE TABLE payment_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  payment_intent_id uuid NOT NULL REFERENCES payment_intents(id),
  type text NOT NULL CHECK (type IN ('created', 'status_changed', 'amount_mismatch', 'wallet_unavailable', 'duplicate', 'late', 'overpayment', 'underpayment', 'reconciled')),
  previous_status text,
  status text,
  amount_minor bigint,
  currency text,
  provider_event_id text,
  details jsonb NOT NULL DEFAULT '{}'::jsonb,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE NULLS NOT DISTINCT (payment_intent_id, provider_event_id, type)
);

CREATE TABLE provider_webhook_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  provider text NOT NULL,
  provider_event_id text NOT NULL,
  signature_valid boolean NOT NULL,
  body_sha256 bytea NOT NULL,
  raw_body bytea NOT NULL,
  headers jsonb NOT NULL DEFAULT '{}'::jsonb,
  status text NOT NULL DEFAULT 'received' CHECK (status IN ('received', 'processed', 'ignored', 'failed')),
  error_code text,
  received_at timestamptz NOT NULL DEFAULT now(),
  processed_at timestamptz,
  retain_until timestamptz NOT NULL DEFAULT (now() + interval '30 days'),
  UNIQUE (provider, provider_event_id)
);

CREATE TABLE refunds (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  payment_intent_id uuid NOT NULL REFERENCES payment_intents(id),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'succeeded', 'failed', 'cancelled')),
  amount_minor bigint NOT NULL CHECK (amount_minor > 0),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  provider_reference text,
  reason text NOT NULL,
  idempotency_key text NOT NULL,
  receipt_metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (payment_intent_id, idempotency_key)
);

CREATE TABLE manual_payment_approvals (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  payment_intent_id uuid NOT NULL REFERENCES payment_intents(id),
  decision text NOT NULL CHECK (decision IN ('approved', 'rejected')),
  operator_id text NOT NULL,
  reason text NOT NULL,
  idempotency_key text NOT NULL,
  request_id text,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (payment_intent_id)
);

CREATE TABLE ledger_transactions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  type text NOT NULL CHECK (type IN ('credit', 'debit', 'payment', 'refund', 'referral_reward', 'correction', 'expiration')),
  reference_type text NOT NULL,
  reference_id text NOT NULL,
  idempotency_key text NOT NULL UNIQUE,
  reason text,
  actor_id text,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (reference_type, reference_id, type)
);

CREATE TABLE ledger_entries (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  transaction_id uuid NOT NULL REFERENCES ledger_transactions(id),
  account_type text NOT NULL CHECK (account_type IN ('customer_wallet', 'platform_clearing', 'provider_clearing')),
  user_id uuid REFERENCES users(id),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  amount_minor bigint NOT NULL CHECK (amount_minor <> 0),
  expires_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  CHECK ((account_type = 'customer_wallet' AND user_id IS NOT NULL) OR (account_type IN ('platform_clearing', 'provider_clearing') AND user_id IS NULL)),
  UNIQUE (transaction_id, account_type, user_id, currency)
);

CREATE INDEX ledger_entries_balance_idx ON ledger_entries (user_id, currency, created_at)
  WHERE account_type = 'customer_wallet';

CREATE TABLE entitlements (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id),
  order_id uuid NOT NULL REFERENCES orders(id),
  plan_version_id uuid NOT NULL REFERENCES plan_versions(id),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'active', 'limited', 'disabled', 'expired', 'superseded', 'failed')),
  starts_at timestamptz NOT NULL,
  ends_at timestamptz NOT NULL,
  traffic_allowance_bytes bigint CHECK (traffic_allowance_bytes IS NULL OR traffic_allowance_bytes >= 0),
  device_limit integer CHECK (device_limit IS NULL OR device_limit >= 0),
  remnawave_squad_ids uuid[] NOT NULL DEFAULT '{}',
  remnawave_user_id bigint,
  observed_state jsonb NOT NULL DEFAULT '{}'::jsonb,
  reconciled_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (ends_at > starts_at),
  UNIQUE (order_id)
);

CREATE TABLE fulfillment_operations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  entitlement_id uuid NOT NULL REFERENCES entitlements(id),
  operation text NOT NULL CHECK (operation IN ('create', 'extend', 'enable', 'disable', 'reset_traffic', 'set_limits', 'set_squads', 'reconcile')),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'succeeded', 'retrying', 'failed', 'cancelled')),
  idempotency_key text NOT NULL UNIQUE,
  correlation_id text NOT NULL,
  desired_state jsonb NOT NULL DEFAULT '{}'::jsonb,
  attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  last_error_code text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz
);

CREATE INDEX fulfillment_operations_pending_idx
  ON fulfillment_operations (next_attempt_at) WHERE status IN ('pending', 'retrying');

CREATE TABLE fulfillment_history (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  operation_id uuid NOT NULL REFERENCES fulfillment_operations(id),
  status text NOT NULL,
  correlation_id text NOT NULL,
  request_summary jsonb NOT NULL DEFAULT '{}'::jsonb,
  response_summary jsonb NOT NULL DEFAULT '{}'::jsonb,
  error_code text,
  occurred_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE entitlement_drifts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  entitlement_id uuid NOT NULL REFERENCES entitlements(id),
  kind text NOT NULL CHECK (kind IN ('missing_remote', 'external_edit', 'status', 'expiry', 'traffic', 'device_limit', 'squads')),
  expected jsonb NOT NULL,
  observed jsonb NOT NULL,
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved', 'ignored')),
  detected_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz
);

CREATE INDEX entitlement_drifts_open_idx ON entitlement_drifts (detected_at) WHERE status = 'open';
CREATE UNIQUE INDEX entitlement_drifts_one_open_kind_idx
  ON entitlement_drifts (entitlement_id, kind) WHERE status = 'open';

CREATE TABLE audit_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  actor_type text NOT NULL CHECK (actor_type IN ('customer', 'operator', 'system', 'provider')),
  actor_id text,
  action text NOT NULL,
  target_type text NOT NULL,
  target_id text NOT NULL,
  reason text,
  request_id text,
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  occurred_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_events_target_idx ON audit_events (target_type, target_id, occurred_at DESC);
