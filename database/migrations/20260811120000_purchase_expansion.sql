-- Omniflow v0.5 purchase expansion and production runtime.
--
-- Adds wallet top-up, deferred carts, subscription add-ons, plan-scoped squad
-- sets, optional concurrent subscriptions, operator notification topics,
-- encrypted backups, and maintenance mode.
--
-- Financial, audit, provider-event, backup, and fulfillment-history rows stay
-- append-only. Rows that carry a documented lifecycle (carts, subscriptions,
-- maintenance state, operator topics) may be updated in place.

-- ---------------------------------------------------------------------------
-- Concurrent subscriptions
-- ---------------------------------------------------------------------------

-- One customer may own several Remnawave users. A subscription is the stable,
-- customer-visible handle for exactly one of them. Single-subscription
-- installations simply keep one row per customer and never show a picker.
CREATE TABLE subscriptions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id),
  slot integer NOT NULL CHECK (slot > 0),
  label text NOT NULL CHECK (char_length(label) BETWEEN 1 AND 40),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'closed')),
  remnawave_user_id bigint CHECK (remnawave_user_id IS NULL OR remnawave_user_id > 0),
  remnawave_username text CHECK (remnawave_username IS NULL OR char_length(remnawave_username) BETWEEN 1 AND 64),
  observed_state jsonb NOT NULL DEFAULT '{}'::jsonb,
  reconciled_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  closed_at timestamptz,
  UNIQUE (user_id, slot)
);

CREATE UNIQUE INDEX subscriptions_remnawave_user_idx
  ON subscriptions (remnawave_user_id) WHERE remnawave_user_id IS NOT NULL;
CREATE INDEX subscriptions_active_idx ON subscriptions (user_id, slot) WHERE status = 'active';

-- Every existing customer keeps its current Remnawave user as slot 1, so an
-- upgraded installation behaves exactly as it did before.
INSERT INTO subscriptions (user_id, slot, label, remnawave_user_id, observed_state, reconciled_at)
SELECT r.user_id, 1, 'Subscription 1', r.remnawave_id, r.observed_state, r.reconciled_at
FROM remnawave_users r;

INSERT INTO subscriptions (user_id, slot, label)
SELECT DISTINCT e.user_id, 1, 'Subscription 1'
FROM entitlements e
WHERE NOT EXISTS (SELECT 1 FROM subscriptions s WHERE s.user_id = e.user_id);

ALTER TABLE entitlements
  ADD COLUMN subscription_id uuid REFERENCES subscriptions(id);

UPDATE entitlements e SET subscription_id = s.id
FROM subscriptions s WHERE s.user_id = e.user_id AND s.slot = 1;

CREATE INDEX entitlements_subscription_idx ON entitlements (subscription_id, ends_at DESC);

-- An order names the subscription it changes. Top-up orders leave it null
-- because a wallet belongs to the customer, never to one subscription.
ALTER TABLE orders
  ADD COLUMN subscription_id uuid REFERENCES subscriptions(id),
  ADD COLUMN selected_squad_ids uuid[] NOT NULL DEFAULT '{}';

UPDATE orders o SET subscription_id = s.id
FROM subscriptions s WHERE s.user_id = o.user_id AND s.slot = 1;

CREATE INDEX orders_subscription_idx ON orders (subscription_id, created_at DESC);

-- Alerts are keyed per subscription so a busy subscription cannot suppress a
-- quiet one's expiry or traffic notice.
ALTER TABLE notification_deliveries
  ADD COLUMN subscription_id uuid REFERENCES subscriptions(id);

ALTER TABLE notification_deliveries
  DROP CONSTRAINT notification_deliveries_user_id_kind_dedupe_key_key;

CREATE UNIQUE INDEX notification_deliveries_dedupe_idx
  ON notification_deliveries (user_id, kind, subscription_id, dedupe_key) NULLS NOT DISTINCT;

-- Auto-renew targets one subscription. A customer with three subscriptions may
-- renew two automatically and keep the third manual.
ALTER TABLE auto_renew_settings
  ADD COLUMN subscription_id uuid REFERENCES subscriptions(id);

UPDATE auto_renew_settings a SET subscription_id = s.id
FROM subscriptions s WHERE s.user_id = a.user_id AND s.slot = 1;

ALTER TABLE auto_renew_settings DROP CONSTRAINT auto_renew_settings_pkey;

CREATE UNIQUE INDEX auto_renew_settings_target_idx
  ON auto_renew_settings (user_id, subscription_id) NULLS NOT DISTINCT;

-- A plan may cap how many concurrent subscriptions of that plan one customer
-- holds, independently of the installation-wide limit.
ALTER TABLE plans
  ADD COLUMN max_concurrent_per_customer integer
    CHECK (max_concurrent_per_customer IS NULL OR max_concurrent_per_customer > 0);

-- ---------------------------------------------------------------------------
-- Plan-scoped squad sets
-- ---------------------------------------------------------------------------

-- plan_versions.remnawave_squad_ids stays the always-assigned set. The rows
-- below are the additional squads a customer may choose from.
ALTER TABLE plan_versions
  ADD COLUMN squad_selection text NOT NULL DEFAULT 'automatic'
    CHECK (squad_selection IN ('automatic', 'optional', 'required')),
  ADD COLUMN min_selectable_squads integer NOT NULL DEFAULT 0 CHECK (min_selectable_squads >= 0),
  ADD COLUMN max_selectable_squads integer
    CHECK (max_selectable_squads IS NULL OR max_selectable_squads > 0),
  ADD CONSTRAINT plan_versions_squad_selection_check
    CHECK (max_selectable_squads IS NULL OR max_selectable_squads >= min_selectable_squads);

CREATE TABLE plan_version_squads (
  plan_version_id uuid NOT NULL REFERENCES plan_versions(id),
  squad_id uuid NOT NULL,
  label_ru text NOT NULL CHECK (char_length(label_ru) BETWEEN 1 AND 80),
  label_en text NOT NULL CHECK (char_length(label_en) BETWEEN 1 AND 80),
  sort_order integer NOT NULL DEFAULT 0,
  PRIMARY KEY (plan_version_id, squad_id)
);

-- ---------------------------------------------------------------------------
-- Add-ons
-- ---------------------------------------------------------------------------

CREATE TABLE addons (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  code text NOT NULL UNIQUE CHECK (code ~ '^[a-z][a-z0-9_-]{1,63}$'),
  kind text NOT NULL CHECK (kind IN ('traffic', 'devices', 'squads')),
  visible boolean NOT NULL DEFAULT false,
  sort_order integer NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  archived_at timestamptz
);

CREATE TABLE addon_localizations (
  addon_id uuid NOT NULL REFERENCES addons(id),
  locale text NOT NULL CHECK (locale IN ('ru', 'en')),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
  PRIMARY KEY (addon_id, locale)
);

-- An add-on version is immutable once an order references it, exactly like a
-- plan version, so a historical order never changes when pricing changes.
CREATE TABLE addon_versions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  addon_id uuid NOT NULL REFERENCES addons(id),
  version integer NOT NULL CHECK (version > 0),
  traffic_bytes bigint CHECK (traffic_bytes IS NULL OR traffic_bytes > 0),
  device_slots integer CHECK (device_slots IS NULL OR device_slots > 0),
  remnawave_squad_ids uuid[] NOT NULL DEFAULT '{}',
  max_quantity integer NOT NULL DEFAULT 1 CHECK (max_quantity > 0),
  proration text NOT NULL DEFAULT 'full_price'
    CHECK (proration IN ('full_price', 'remaining_period', 'daily_rate')),
  created_at timestamptz NOT NULL DEFAULT now(),
  retired_at timestamptz,
  UNIQUE (addon_id, version),
  CHECK (traffic_bytes IS NOT NULL OR device_slots IS NOT NULL OR cardinality(remnawave_squad_ids) > 0)
);

CREATE TABLE addon_prices (
  addon_version_id uuid NOT NULL REFERENCES addon_versions(id),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  amount_minor bigint NOT NULL CHECK (amount_minor >= 0),
  PRIMARY KEY (addon_version_id, currency)
);

-- An add-on is offered only for the plan versions that list it.
CREATE TABLE plan_version_addons (
  plan_version_id uuid NOT NULL REFERENCES plan_versions(id),
  addon_id uuid NOT NULL REFERENCES addons(id),
  PRIMARY KEY (plan_version_id, addon_id)
);

CREATE TABLE order_addon_lines (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  order_id uuid NOT NULL REFERENCES orders(id),
  addon_id uuid NOT NULL REFERENCES addons(id),
  addon_version_id uuid NOT NULL REFERENCES addon_versions(id),
  quantity integer NOT NULL DEFAULT 1 CHECK (quantity > 0),
  unit_amount_minor bigint NOT NULL CHECK (unit_amount_minor >= 0),
  charged_minor bigint NOT NULL CHECK (charged_minor >= 0),
  proration text NOT NULL,
  snapshot jsonb NOT NULL,
  UNIQUE (order_id, addon_version_id)
);

-- The applied record is what makes add-on fulfillment idempotent: a replayed
-- settlement finds the row and leaves the entitlement untouched.
CREATE TABLE entitlement_addons (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  entitlement_id uuid NOT NULL REFERENCES entitlements(id),
  order_id uuid NOT NULL REFERENCES orders(id),
  addon_version_id uuid NOT NULL REFERENCES addon_versions(id),
  quantity integer NOT NULL CHECK (quantity > 0),
  traffic_bytes bigint CHECK (traffic_bytes IS NULL OR traffic_bytes >= 0),
  device_slots integer CHECK (device_slots IS NULL OR device_slots >= 0),
  remnawave_squad_ids uuid[] NOT NULL DEFAULT '{}',
  applied_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (order_id, addon_version_id)
);

CREATE INDEX entitlement_addons_entitlement_idx ON entitlement_addons (entitlement_id, applied_at);

-- ---------------------------------------------------------------------------
-- Wallet top-up
-- ---------------------------------------------------------------------------

-- A top-up and an add-on purchase are ordinary orders so they reuse the whole
-- payment, webhook, reconciliation, and refund pipeline unchanged.
ALTER TABLE orders
  DROP CONSTRAINT orders_operation_check,
  ADD CONSTRAINT orders_operation_check
    CHECK (operation IN ('purchase', 'upgrade', 'downgrade', 'extension', 'topup', 'addon'));

CREATE TABLE wallet_topups (
  order_id uuid PRIMARY KEY REFERENCES orders(id),
  user_id uuid NOT NULL REFERENCES users(id),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  requested_minor bigint NOT NULL CHECK (requested_minor > 0),
  credited_minor bigint NOT NULL DEFAULT 0 CHECK (credited_minor >= 0),
  ledger_transaction_id uuid REFERENCES ledger_transactions(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  credited_at timestamptz,
  CHECK ((ledger_transaction_id IS NULL) = (credited_at IS NULL))
);

CREATE INDEX wallet_topups_history_idx ON wallet_topups (user_id, created_at DESC);
CREATE INDEX wallet_topups_window_idx ON wallet_topups (user_id, currency, credited_at)
  WHERE credited_at IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Cart and deferred purchase
-- ---------------------------------------------------------------------------

CREATE TABLE carts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  subscription_id uuid REFERENCES subscriptions(id),
  plan_version_id uuid NOT NULL REFERENCES plan_versions(id),
  operation text NOT NULL CHECK (operation IN ('purchase', 'upgrade', 'downgrade', 'extension')),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  promo_code text CHECK (promo_code IS NULL OR promo_code ~ '^[A-Z0-9_-]{3,64}$'),
  selected_squad_ids uuid[] NOT NULL DEFAULT '{}',
  auto_purchase boolean NOT NULL DEFAULT true,
  status text NOT NULL DEFAULT 'open'
    CHECK (status IN ('open', 'purchased', 'cancelled', 'expired')),
  idempotency_key text NOT NULL UNIQUE,
  order_id uuid REFERENCES orders(id),
  last_failure text,
  attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL DEFAULT (now() + interval '30 days'),
  CHECK ((status = 'purchased') = (order_id IS NOT NULL))
);

CREATE UNIQUE INDEX carts_one_open_per_customer_idx ON carts (user_id) WHERE status = 'open';
CREATE INDEX carts_expiry_idx ON carts (expires_at) WHERE status = 'open';

CREATE TABLE cart_addons (
  cart_id uuid NOT NULL REFERENCES carts(id) ON DELETE CASCADE,
  addon_version_id uuid NOT NULL REFERENCES addon_versions(id),
  quantity integer NOT NULL DEFAULT 1 CHECK (quantity > 0),
  PRIMARY KEY (cart_id, addon_version_id)
);

-- ---------------------------------------------------------------------------
-- Bot checkout: subscription target, squad selection, and add-ons
-- ---------------------------------------------------------------------------

ALTER TABLE bot_checkout_sessions
  ADD COLUMN subscription_id uuid REFERENCES subscriptions(id),
  ADD COLUMN selected_squad_ids uuid[] NOT NULL DEFAULT '{}';

CREATE TABLE bot_checkout_addons (
  checkout_id uuid NOT NULL REFERENCES bot_checkout_sessions(id) ON DELETE CASCADE,
  addon_version_id uuid NOT NULL REFERENCES addon_versions(id),
  quantity integer NOT NULL DEFAULT 1 CHECK (quantity > 0),
  PRIMARY KEY (checkout_id, addon_version_id)
);

ALTER TABLE bot_sessions
  DROP CONSTRAINT bot_sessions_state_check,
  ADD CONSTRAINT bot_sessions_state_check
    CHECK (state IN ('support_message', 'support_reply', 'promo_code',
                     'topup_amount', 'subscription_label'));

-- ---------------------------------------------------------------------------
-- Operator notifications
-- ---------------------------------------------------------------------------

-- The operator supplies a group. The bot creates and binds every forum topic
-- itself, and re-creates one that was deleted.
CREATE TABLE operator_topics (
  kind text PRIMARY KEY CHECK (kind IN ('purchase', 'renewal', 'topup', 'refund',
                                        'fulfillment_failure', 'incident', 'backup')),
  chat_id bigint NOT NULL,
  topic_id bigint CHECK (topic_id IS NULL OR topic_id > 0),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'bound', 'failed')),
  last_error_code text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK ((status = 'bound') = (topic_id IS NOT NULL))
);

-- Operator notices never carry customer content, secrets, or payment payloads.
CREATE TABLE operator_notifications (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  kind text NOT NULL CHECK (kind IN ('purchase', 'renewal', 'topup', 'refund',
                                     'fulfillment_failure', 'incident', 'backup')),
  dedupe_key text NOT NULL,
  status text NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'sent', 'suppressed', 'failed')),
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  error_code text,
  created_at timestamptz NOT NULL DEFAULT now(),
  sent_at timestamptz,
  UNIQUE (kind, dedupe_key)
);

CREATE INDEX operator_notifications_pending_idx
  ON operator_notifications (created_at) WHERE status = 'pending';
CREATE INDEX operator_notifications_volume_idx
  ON operator_notifications (kind, sent_at DESC) WHERE status = 'sent';

-- ---------------------------------------------------------------------------
-- Backups
-- ---------------------------------------------------------------------------

CREATE TABLE backups (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  kind text NOT NULL DEFAULT 'scheduled' CHECK (kind IN ('scheduled', 'manual')),
  status text NOT NULL DEFAULT 'running'
    CHECK (status IN ('running', 'completed', 'failed', 'pruned')),
  file_name text NOT NULL UNIQUE CHECK (file_name ~ '^[A-Za-z0-9._-]{1,128}$'),
  size_bytes bigint NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
  sha256 bytea,
  encrypted boolean NOT NULL DEFAULT true,
  verified_at timestamptz,
  error_code text,
  requested_by text,
  started_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  retain_until timestamptz NOT NULL
);

CREATE INDEX backups_retention_idx ON backups (retain_until) WHERE status = 'completed';
CREATE INDEX backups_recent_idx ON backups (started_at DESC);

CREATE TABLE backup_restores (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  backup_id uuid NOT NULL REFERENCES backups(id),
  status text NOT NULL DEFAULT 'requested'
    CHECK (status IN ('requested', 'running', 'completed', 'failed')),
  operator_id text NOT NULL,
  reason text NOT NULL,
  error_code text,
  requested_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz
);

CREATE INDEX backup_restores_backup_idx ON backup_restores (backup_id, requested_at DESC);

-- ---------------------------------------------------------------------------
-- Maintenance mode
-- ---------------------------------------------------------------------------

-- Maintenance blocks new purchases and defers fulfillment. Money already taken
-- keeps its state: nothing here cancels, refunds, or expires a paid order.
CREATE TABLE maintenance_state (
  singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  active boolean NOT NULL DEFAULT false,
  source text NOT NULL DEFAULT 'manual'
    CHECK (source IN ('manual', 'remnawave', 'database', 'valkey')),
  reason text NOT NULL DEFAULT '' CHECK (char_length(reason) <= 200),
  notice_ru text NOT NULL DEFAULT '' CHECK (char_length(notice_ru) <= 1000),
  notice_en text NOT NULL DEFAULT '' CHECK (char_length(notice_en) <= 1000),
  expected_return_at timestamptz,
  activated_at timestamptz,
  cleared_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (NOT active OR activated_at IS NOT NULL)
);

-- Every activation and recovery is kept so an operator can explain an outage.
CREATE TABLE maintenance_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  action text NOT NULL CHECK (action IN ('activated', 'cleared')),
  source text NOT NULL CHECK (source IN ('manual', 'remnawave', 'database', 'valkey')),
  reason text NOT NULL DEFAULT '',
  actor_type text NOT NULL CHECK (actor_type IN ('operator', 'system')),
  actor_id text,
  occurred_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX maintenance_events_recent_idx ON maintenance_events (occurred_at DESC);
