-- Omniflow v0.4 Telegram commerce, subscription lifecycle, support desk,
-- communication, and abuse-control model.
-- Financial, consent, audit, reward, and delivery records are append-only unless
-- a documented lifecycle explicitly permits mutation.

-- Multi-step bot flows need a state payload that never stores secrets.
ALTER TABLE bot_sessions
  DROP CONSTRAINT bot_sessions_state_check,
  ADD CONSTRAINT bot_sessions_state_check
    CHECK (state IN ('support_message', 'support_reply', 'promo_code')),
  ADD COLUMN context jsonb NOT NULL DEFAULT '{}'::jsonb;

-- Marketing consent, communication classes, and quiet hours are per customer.
ALTER TABLE bot_preferences
  ADD COLUMN renewal_notifications boolean NOT NULL DEFAULT true,
  ADD COLUMN news_notifications boolean NOT NULL DEFAULT true,
  ADD COLUMN marketing_enabled boolean NOT NULL DEFAULT false,
  ADD COLUMN quiet_hours_start smallint CHECK (quiet_hours_start BETWEEN 0 AND 23),
  ADD COLUMN quiet_hours_end smallint CHECK (quiet_hours_end BETWEEN 0 AND 23),
  ADD CONSTRAINT bot_preferences_quiet_hours_check
    CHECK ((quiet_hours_start IS NULL) = (quiet_hours_end IS NULL));

ALTER TABLE notification_deliveries
  DROP CONSTRAINT notification_deliveries_kind_check,
  DROP CONSTRAINT notification_deliveries_status_check,
  ADD CONSTRAINT notification_deliveries_status_check
    CHECK (status IN ('pending', 'deferred', 'sent', 'failed', 'suppressed')),
  ADD CONSTRAINT notification_deliveries_kind_check
    CHECK (kind IN ('expiry', 'traffic', 'renewal', 'grace', 'recovery', 'payment',
                    'fulfillment', 'support', 'news', 'announcement', 'incident',
                    'maintenance', 'marketing', 'referral', 'trial')),
  ADD COLUMN class text NOT NULL DEFAULT 'transactional'
    CHECK (class IN ('transactional', 'marketing')),
  ADD COLUMN error_code text,
  ADD COLUMN deferred_until timestamptz;

CREATE INDEX notification_deliveries_frequency_idx
  ON notification_deliveries (user_id, class, sent_at DESC) WHERE status = 'sent';

-- Telegram delivery health. Blocked and deactivated accounts stop retrying.
CREATE TABLE bot_delivery_state (
  user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  status text NOT NULL DEFAULT 'active'
    CHECK (status IN ('active', 'failing', 'blocked', 'deactivated')),
  last_error_code text,
  consecutive_failures integer NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
  retry_after timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- Plans gain an explicit grace window so limited-state copy is not guesswork.
ALTER TABLE plan_versions
  ADD COLUMN grace_period_seconds bigint NOT NULL DEFAULT 0 CHECK (grace_period_seconds >= 0),
  ADD COLUMN trial_eligibility text NOT NULL DEFAULT 'new_customer'
    CHECK (trial_eligibility IN ('new_customer', 'never_trialled', 'any'));

-- Bot checkout keeps its own resumable session so a duplicate tap cannot create
-- a second order for the same intent.
CREATE TABLE bot_checkout_sessions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  plan_version_id uuid NOT NULL REFERENCES plan_versions(id),
  operation text NOT NULL CHECK (operation IN ('purchase', 'upgrade', 'downgrade', 'extension')),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  provider text CHECK (provider IN ('telegram_stars', 'cryptobot', 'yookassa', 'manual')),
  promo_code text CHECK (promo_code IS NULL OR promo_code ~ '^[A-Z0-9_-]{3,64}$'),
  promo_rejection text,
  apply_wallet boolean NOT NULL DEFAULT true,
  order_id uuid REFERENCES orders(id),
  idempotency_key text NOT NULL UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL DEFAULT (now() + interval '1 hour')
);

CREATE UNIQUE INDEX bot_checkout_sessions_open_idx
  ON bot_checkout_sessions (user_id) WHERE order_id IS NULL;
CREATE INDEX bot_checkout_sessions_expiry_idx ON bot_checkout_sessions (expires_at);

-- One trial per customer, recorded when the trial order is created.
CREATE TABLE trial_claims (
  user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  plan_id uuid NOT NULL REFERENCES plans(id),
  order_id uuid NOT NULL REFERENCES orders(id),
  claimed_at timestamptz NOT NULL DEFAULT now()
);

-- Auto-renew intent. Only providers that advertise recurring support may enable it.
CREATE TABLE auto_renew_settings (
  user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  enabled boolean NOT NULL DEFAULT false,
  plan_version_id uuid REFERENCES plan_versions(id),
  provider text CHECK (provider IN ('telegram_stars', 'cryptobot', 'yookassa', 'manual')),
  currency text CHECK (currency IS NULL OR currency ~ '^[A-Z]{3}$'),
  cancelled_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (NOT enabled OR (plan_version_id IS NOT NULL AND provider IS NOT NULL AND currency IS NOT NULL))
);

-- Support desk: subjects, priority, unread state, and per-message delivery.
ALTER TABLE support_tickets
  ADD COLUMN subject text NOT NULL DEFAULT '' CHECK (char_length(subject) <= 120),
  ADD COLUMN priority text NOT NULL DEFAULT 'normal'
    CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
  ADD COLUMN last_message_at timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN customer_unread_count integer NOT NULL DEFAULT 0 CHECK (customer_unread_count >= 0),
  ADD COLUMN closed_at timestamptz;

CREATE INDEX support_tickets_user_updated_idx ON support_tickets (user_id, updated_at DESC);

ALTER TABLE support_messages
  ADD COLUMN dedupe_key text,
  ADD COLUMN delivered_at timestamptz,
  ADD COLUMN read_at timestamptz;

CREATE UNIQUE INDEX support_messages_dedupe_idx
  ON support_messages (ticket_id, dedupe_key) WHERE dedupe_key IS NOT NULL;

-- Attachments store only Telegram file references and safe metadata.
CREATE TABLE support_attachments (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  message_id bigint NOT NULL REFERENCES support_messages(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('photo', 'document')),
  telegram_file_id text NOT NULL,
  file_name text CHECK (file_name IS NULL OR char_length(file_name) <= 200),
  mime_type text CHECK (mime_type IS NULL OR char_length(mime_type) <= 120),
  size_bytes bigint NOT NULL CHECK (size_bytes > 0 AND size_bytes <= 10485760),
  retain_until timestamptz NOT NULL DEFAULT (now() + interval '90 days'),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (message_id, telegram_file_id)
);

CREATE INDEX support_attachments_retention_idx ON support_attachments (retain_until);

-- News and service announcements.
CREATE TABLE news_posts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9_-]{2,63}$'),
  category text NOT NULL CHECK (category IN ('news', 'announcement', 'incident', 'maintenance')),
  class text NOT NULL DEFAULT 'transactional' CHECK (class IN ('transactional', 'marketing')),
  published_at timestamptz,
  expires_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  CHECK (expires_at IS NULL OR published_at IS NULL OR expires_at > published_at)
);

CREATE INDEX news_posts_published_idx
  ON news_posts (published_at DESC) WHERE published_at IS NOT NULL;

CREATE TABLE news_post_localizations (
  post_id uuid NOT NULL REFERENCES news_posts(id) ON DELETE CASCADE,
  locale text NOT NULL CHECK (locale IN ('ru', 'en')),
  title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
  body text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 3500),
  PRIMARY KEY (post_id, locale)
);

CREATE TABLE news_reads (
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  post_id uuid NOT NULL REFERENCES news_posts(id) ON DELETE CASCADE,
  read_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, post_id)
);

-- Referral program configuration and exactly-once reward records.
CREATE TABLE referral_programs (
  singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  enabled boolean NOT NULL DEFAULT false,
  currency text NOT NULL DEFAULT 'RUB' CHECK (currency ~ '^[A-Z]{3}$'),
  inviter_reward_minor bigint NOT NULL DEFAULT 0 CHECK (inviter_reward_minor >= 0),
  invitee_reward_minor bigint NOT NULL DEFAULT 0 CHECK (invitee_reward_minor >= 0),
  qualification text NOT NULL DEFAULT 'first_paid_order'
    CHECK (qualification IN ('first_paid_order')),
  inviter_reward_cap integer CHECK (inviter_reward_cap IS NULL OR inviter_reward_cap > 0),
  attribution_validity_days integer NOT NULL DEFAULT 90 CHECK (attribution_validity_days > 0),
  reward_expiry_days integer CHECK (reward_expiry_days IS NULL OR reward_expiry_days > 0),
  terms_url text,
  updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE referral_attributions
  ADD COLUMN qualified_at timestamptz,
  ADD COLUMN qualifying_order_id uuid REFERENCES orders(id),
  ADD COLUMN rejected_reason text;

CREATE TABLE referral_rewards (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  referred_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  beneficiary_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role text NOT NULL CHECK (role IN ('inviter', 'invitee')),
  order_id uuid NOT NULL REFERENCES orders(id),
  amount_minor bigint NOT NULL CHECK (amount_minor > 0),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  ledger_transaction_id uuid NOT NULL REFERENCES ledger_transactions(id),
  granted_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (referred_user_id, role)
);

CREATE INDEX referral_rewards_beneficiary_idx
  ON referral_rewards (beneficiary_user_id, granted_at DESC);
