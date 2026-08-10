CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE users (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'deleted')),
  locale text NOT NULL DEFAULT 'ru' CHECK (locale IN ('ru', 'en')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE identities (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id),
  provider text NOT NULL,
  provider_subject text NOT NULL,
  verified_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (provider, provider_subject)
);

CREATE TABLE remnawave_users (
  user_id uuid PRIMARY KEY REFERENCES users(id),
  remnawave_id bigint NOT NULL UNIQUE CHECK (remnawave_id > 0),
  telegram_id bigint UNIQUE,
  observed_state jsonb NOT NULL DEFAULT '{}'::jsonb,
  reconciled_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE telemetry_installation (
  singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  installation_id uuid NOT NULL DEFAULT gen_random_uuid(),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE telemetry_events (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  installation_id uuid NOT NULL,
  name text NOT NULL CHECK (name IN ('installation.heartbeat', 'feature.usage')),
  version text NOT NULL,
  service text NOT NULL,
  os text NOT NULL,
  architecture text NOT NULL,
  features jsonb NOT NULL DEFAULT '{}'::jsonb,
  counters jsonb NOT NULL DEFAULT '{}'::jsonb,
  occurred_at timestamptz NOT NULL,
  received_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX telemetry_events_received_at_idx ON telemetry_events (received_at);
CREATE INDEX telemetry_events_installation_idx ON telemetry_events (installation_id, received_at DESC);

CREATE TABLE outbox_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  topic text NOT NULL,
  payload jsonb NOT NULL,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  published_at timestamptz
);

CREATE INDEX outbox_events_unpublished_idx ON outbox_events (occurred_at) WHERE published_at IS NULL;

CREATE TABLE bot_preferences (
  user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  locale text NOT NULL DEFAULT 'auto' CHECK (locale IN ('auto', 'ru', 'en')),
  expiry_notifications boolean NOT NULL DEFAULT true,
  traffic_notifications boolean NOT NULL DEFAULT true,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE bot_sessions (
  telegram_id bigint PRIMARY KEY,
  state text NOT NULL CHECK (state IN ('support_message')),
  updated_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL DEFAULT (now() + interval '30 minutes')
);

CREATE TABLE support_tickets (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX support_tickets_one_open_per_user_idx ON support_tickets (user_id) WHERE status = 'open';

CREATE TABLE support_messages (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  ticket_id uuid NOT NULL REFERENCES support_tickets(id) ON DELETE CASCADE,
  sender text NOT NULL CHECK (sender IN ('customer', 'operator', 'system')),
  body text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 4000),
  telegram_message_id bigint,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX support_messages_ticket_idx ON support_messages (ticket_id, created_at);

CREATE TABLE referral_codes (
  user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  code text NOT NULL UNIQUE CHECK (code ~ '^[A-Z0-9]{10}$'),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE referral_attributions (
  referred_user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  referrer_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CHECK (referred_user_id <> referrer_user_id)
);

CREATE INDEX referral_attributions_referrer_idx ON referral_attributions (referrer_user_id, created_at);

CREATE TABLE notification_deliveries (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('expiry', 'traffic')),
  dedupe_key text NOT NULL,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed')),
  scheduled_at timestamptz NOT NULL DEFAULT now(),
  sent_at timestamptz,
  failure_count integer NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
  UNIQUE (user_id, kind, dedupe_key)
);

CREATE INDEX notification_deliveries_pending_idx ON notification_deliveries (scheduled_at) WHERE status = 'pending';
