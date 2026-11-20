-- Named API tokens for the /v1/admin surface, beside the single
-- APP_OPERATOR_TOKEN, each with its own request log; and outgoing webhooks
-- that tell an operator's systems about orders, top-ups, and add-ons.

CREATE TABLE api_tokens (
  id uuid DEFAULT gen_random_uuid() NOT NULL,
  name text NOT NULL,
  -- SHA-256 of the token. The token itself is shown once and never stored.
  token_hash bytea NOT NULL,
  -- The first characters, so an operator can tell tokens apart.
  prefix text NOT NULL,
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz,
  revoked_at timestamptz,
  CONSTRAINT api_tokens_pkey PRIMARY KEY (id),
  CONSTRAINT api_tokens_hash_key UNIQUE (token_hash),
  CONSTRAINT api_tokens_created_by_fkey FOREIGN KEY (created_by) REFERENCES admin_users(id) ON DELETE SET NULL,
  CONSTRAINT api_tokens_name_length CHECK (char_length(name) BETWEEN 1 AND 80)
);

CREATE TABLE api_token_requests (
  id bigint GENERATED ALWAYS AS IDENTITY,
  token_id uuid NOT NULL,
  method text NOT NULL,
  route text NOT NULL,
  status integer NOT NULL,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT api_token_requests_pkey PRIMARY KEY (id),
  CONSTRAINT api_token_requests_token_fkey FOREIGN KEY (token_id) REFERENCES api_tokens(id) ON DELETE CASCADE
);
CREATE INDEX api_token_requests_token_idx ON api_token_requests (token_id, occurred_at DESC);
CREATE INDEX api_token_requests_time_idx ON api_token_requests (occurred_at);

CREATE TABLE webhook_endpoints (
  id uuid DEFAULT gen_random_uuid() NOT NULL,
  name text NOT NULL,
  url text NOT NULL,
  events text[] NOT NULL,
  -- The signing secret, sealed with APP_DATA_ENCRYPTION_KEY.
  secret_ciphertext bytea NOT NULL,
  enabled boolean NOT NULL DEFAULT true,
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT webhook_endpoints_pkey PRIMARY KEY (id),
  CONSTRAINT webhook_endpoints_created_by_fkey FOREIGN KEY (created_by) REFERENCES admin_users(id) ON DELETE SET NULL,
  CONSTRAINT webhook_endpoints_name_length CHECK (char_length(name) BETWEEN 1 AND 80),
  CONSTRAINT webhook_endpoints_url_https CHECK (url ~ '^https://'),
  CONSTRAINT webhook_endpoints_events_present CHECK (cardinality(events) >= 1)
);

CREATE TABLE webhook_deliveries (
  id uuid DEFAULT gen_random_uuid() NOT NULL,
  endpoint_id uuid NOT NULL,
  event_id uuid NOT NULL,
  topic text NOT NULL,
  payload jsonb NOT NULL,
  status text NOT NULL DEFAULT 'pending',
  attempts integer NOT NULL DEFAULT 0,
  response_status integer,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now(),
  delivered_at timestamptz,
  CONSTRAINT webhook_deliveries_pkey PRIMARY KEY (id),
  CONSTRAINT webhook_deliveries_once UNIQUE (endpoint_id, event_id),
  CONSTRAINT webhook_deliveries_endpoint_fkey FOREIGN KEY (endpoint_id) REFERENCES webhook_endpoints(id) ON DELETE CASCADE,
  CONSTRAINT webhook_deliveries_status_known CHECK (status = ANY (ARRAY['pending', 'delivered', 'failed']::text[]))
);
CREATE INDEX webhook_deliveries_due_idx ON webhook_deliveries (next_attempt_at) WHERE status = 'pending';
CREATE INDEX webhook_deliveries_endpoint_idx ON webhook_deliveries (endpoint_id, created_at DESC);
