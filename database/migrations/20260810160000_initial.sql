-- Create the initial Omniflow identity, Remnawave mapping, telemetry, and outbox schema.
-- atlas:txmode file

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

INSERT INTO telemetry_installation (singleton) VALUES (true);

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
