-- AI governance, MCP connections, and the installation settings behind them.
--
-- Three concerns in one migration because they are one decision: an owner
-- turning on an assistant is choosing a provider, a budget, a retention policy,
-- and a set of connections at the same time, and splitting the tables would not
-- split the choice.
--
-- Two rules run through all of it. Nothing is on until an owner turns it on,
-- expressed as `enabled boolean NOT NULL DEFAULT false` rather than as
-- application logic. And no prompt, output, ticket body, or tool argument is
-- stored in a metrics or telemetry table — the usage tables below carry counts,
-- durations, and money, and there is deliberately no column to put content in.

-- Installation settings an owner edits in the panel.
--
-- One row per section rather than one row per key: a section is what a screen
-- saves, and saving a screen atomically is the difference between a half-applied
-- Remnawave connection and none.
CREATE TABLE installation_settings (
  section text PRIMARY KEY CHECK (section IN (
    'branding', 'remnawave', 'telegram', 'operator_group', 'required_channels',
    'maintenance', 'notifications', 'telemetry', 'backup', 'security', 'ai', 'mcp'
  )),

  -- document holds everything an operator may read back. Secrets never live
  -- here, which is what makes "return the section" a safe operation.
  document jsonb NOT NULL DEFAULT '{}'::jsonb,

  -- secrets_ciphertext is the sealed companion: tokens, webhook secrets, API
  -- keys. It is write-only by construction — no query in this repository selects
  -- it into a response type, and the diagnostics bundle excludes the column
  -- rather than redacting its value.
  secrets_ciphertext bytea,

  -- version increments on every write so a concurrent edit from two panel tabs
  -- is a conflict rather than a silent overwrite.
  version integer NOT NULL DEFAULT 1 CHECK (version > 0),

  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid REFERENCES admin_users(id)
);

COMMENT ON COLUMN installation_settings.secrets_ciphertext IS
  'Sealed with APP_DATA_ENCRYPTION_KEY. Never returned to a client, never included in a diagnostics bundle.';

-- Providers an owner has approved.
--
-- Approval is a row. A task configured for a provider with no row here cannot
-- run, which is what makes "fallback cannot widen where data goes" enforceable
-- rather than aspirational.
CREATE TABLE ai_providers (
  slug text PRIMARY KEY CHECK (slug ~ '^[a-z0-9][a-z0-9_-]{1,40}$'),
  kind text NOT NULL CHECK (kind IN ('openai_compatible', 'anthropic', 'gemini')),
  display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 120),

  -- base_url is stored for the OpenAI-compatible adapter, which is how an owner
  -- points at a local model. It is plain text: it is an address, not a secret,
  -- and an owner needs to see which one is configured.
  base_url text CHECK (base_url IS NULL OR char_length(base_url) <= 400),
  credentials_ciphertext bytea,

  enabled boolean NOT NULL DEFAULT false,

  -- What the provider does with the data, recorded so the panel can warn before
  -- a feature is switched on rather than after. These are the owner's answers
  -- from the provider's own terms; Omniflow cannot verify them and does not
  -- pretend to.
  zero_retention boolean NOT NULL DEFAULT false,
  trains_on_data boolean NOT NULL DEFAULT false,
  retention_notice text CHECK (retention_notice IS NULL OR char_length(retention_notice) <= 2000),
  -- data_region matters for an installation with a jurisdictional constraint.
  data_region text CHECK (data_region IS NULL OR char_length(data_region) <= 60),

  -- The last connection test, so "is this configured correctly?" is answerable
  -- without spending a real request.
  last_checked_at timestamptz,
  last_check_ok boolean,
  last_check_detail text CHECK (last_check_detail IS NULL OR char_length(last_check_detail) <= 500),

  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid REFERENCES admin_users(id)
);

-- Per-feature enablement and configuration.
--
-- A feature is finer than a task: "AI-assisted support" is what an owner turns
-- on, and it maps to several tasks. Both exist because the budget belongs to the
-- task and the decision belongs to the feature.
CREATE TABLE ai_features (
  feature text PRIMARY KEY CHECK (feature IN (
    'support_summary', 'support_reply', 'support_rewrite', 'support_translate',
    'support_classify', 'marketing_draft', 'risk_analysis', 'copilot', 'mcp_tools'
  )),
  enabled boolean NOT NULL DEFAULT false,

  provider_slug text REFERENCES ai_providers(slug) ON DELETE RESTRICT,
  model text CHECK (model IS NULL OR char_length(model) <= 120),
  temperature numeric(3, 2) CHECK (temperature IS NULL OR temperature BETWEEN 0 AND 2),
  max_tokens integer CHECK (max_tokens IS NULL OR max_tokens BETWEEN 1 AND 200000),
  timeout_ms integer CHECK (timeout_ms IS NULL OR timeout_ms BETWEEN 1000 AND 600000),

  budget_tokens bigint CHECK (budget_tokens IS NULL OR budget_tokens >= 0),
  budget_window_seconds integer CHECK (budget_window_seconds IS NULL OR budget_window_seconds > 0),
  budget_cost_minor bigint CHECK (budget_cost_minor IS NULL OR budget_cost_minor >= 0),

  -- Retention is per feature because the material differs: a marketing draft is
  -- the operator's own copy, and a support summary is somebody's complaint.
  retain_prompts boolean NOT NULL DEFAULT false,
  retain_outputs boolean NOT NULL DEFAULT true,
  retention_days integer NOT NULL DEFAULT 30 CHECK (retention_days BETWEEN 0 AND 3650),

  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid REFERENCES admin_users(id),

  -- An enabled feature with no provider would fail on first use and look like a
  -- bug rather than a missing setting.
  CONSTRAINT ai_features_enabled_needs_provider
    CHECK (NOT enabled OR (provider_slug IS NOT NULL AND model IS NOT NULL))
);

-- Usage ceilings by scope.
--
-- Four scopes rather than one global number, because "the installation spent its
-- budget" and "one operator looped a copilot" need different answers, and only
-- the second one should stop that operator.
CREATE TABLE ai_usage_limits (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  scope text NOT NULL CHECK (scope IN ('installation', 'role', 'operator', 'feature')),
  -- scope_ref is the role name or operator id; null for installation-wide.
  scope_ref text CHECK (char_length(scope_ref) <= 120),
  feature text CHECK (feature IS NULL OR char_length(feature) <= 60),

  window_seconds integer NOT NULL DEFAULT 86400 CHECK (window_seconds > 0),
  max_requests integer CHECK (max_requests IS NULL OR max_requests >= 0),
  max_tokens bigint CHECK (max_tokens IS NULL OR max_tokens >= 0),
  max_cost_minor bigint CHECK (max_cost_minor IS NULL OR max_cost_minor >= 0),

  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid REFERENCES admin_users(id),

  CONSTRAINT ai_usage_limits_scope_ref_shape CHECK (
    (scope = 'installation' AND scope_ref IS NULL)
    OR (scope IN ('role', 'operator') AND scope_ref IS NOT NULL)
    OR (scope = 'feature' AND scope_ref IS NULL AND feature IS NOT NULL)
  ),
  -- A limit with no ceiling on anything is a row that does nothing and reads as
  -- protection.
  CONSTRAINT ai_usage_limits_bounds_something CHECK (
    max_requests IS NOT NULL OR max_tokens IS NOT NULL OR max_cost_minor IS NOT NULL
  )
);

CREATE UNIQUE INDEX ai_usage_limits_scope_idx
  ON ai_usage_limits (scope, coalesce(scope_ref, ''), coalesce(feature, ''));

-- One row per model request.
--
-- There is no prompt column and no output column, and that is the point: this is
-- the table metrics and cost reports read, and a metrics table that can hold a
-- ticket body eventually holds one.
CREATE TABLE ai_usage_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  occurred_at timestamptz NOT NULL DEFAULT now(),

  feature text NOT NULL CHECK (char_length(feature) BETWEEN 1 AND 60),
  task text NOT NULL CHECK (char_length(task) BETWEEN 1 AND 60),
  provider_slug text NOT NULL CHECK (char_length(provider_slug) BETWEEN 1 AND 60),
  model text NOT NULL CHECK (char_length(model) BETWEEN 1 AND 120),

  operator_id uuid REFERENCES admin_users(id) ON DELETE SET NULL,
  -- operator_role is denormalised so a per-role report survives a role change,
  -- and so the report does not need to join a table an analyst may not read.
  operator_role text CHECK (operator_role IS NULL OR char_length(operator_role) <= 60),

  input_tokens integer NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
  output_tokens integer NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
  latency_ms integer NOT NULL DEFAULT 0 CHECK (latency_ms >= 0),
  -- Estimated rather than billed. Omniflow does not see the invoice, and a
  -- column named cost_minor without "estimated" nearby invites somebody to
  -- reconcile against it.
  estimated_cost_minor bigint NOT NULL DEFAULT 0 CHECK (estimated_cost_minor >= 0),
  currency text CHECK (currency IS NULL OR currency ~ '^[A-Z]{3}$'),

  outcome text NOT NULL CHECK (outcome IN (
    'succeeded', 'refused', 'failed', 'timed_out', 'budget_exhausted'
  )),
  -- error_code is a category, never a provider message: a provider's error text
  -- can echo the prompt back.
  error_code text CHECK (error_code IS NULL OR char_length(error_code) <= 60),

  -- redaction_summary is a count per category, so "what left?" is answerable
  -- without keeping what left.
  redaction_summary jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX ai_usage_events_window_idx ON ai_usage_events (occurred_at DESC, feature);
CREATE INDEX ai_usage_events_operator_idx ON ai_usage_events (operator_id, occurred_at DESC)
  WHERE operator_id IS NOT NULL;

-- Decisions an AI feature influenced.
--
-- It exists so an audit export can answer "which of these were shaped by a
-- model?" — a question that becomes urgent exactly once, under conditions where
-- reconstructing it from logs is not good enough.
CREATE TABLE ai_decisions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  occurred_at timestamptz NOT NULL DEFAULT now(),

  subject_type text NOT NULL CHECK (subject_type IN (
    'support_ticket', 'customer', 'order', 'refund', 'campaign', 'news_post',
    'subscription', 'risk_assessment'
  )),
  subject_id text NOT NULL CHECK (char_length(subject_id) BETWEEN 1 AND 120),

  feature text NOT NULL CHECK (char_length(feature) BETWEEN 1 AND 60),
  provider_slug text NOT NULL CHECK (char_length(provider_slug) BETWEEN 1 AND 60),
  model text NOT NULL CHECK (char_length(model) BETWEEN 1 AND 120),
  policy_version text CHECK (policy_version IS NULL OR char_length(policy_version) <= 60),

  -- The operator who decided. A generated draft nobody sent is not a decision,
  -- so this is never null: a row here means a person acted.
  operator_id uuid NOT NULL REFERENCES admin_users(id),

  -- How the suggestion was used. "edited" is separate from "accepted" because
  -- an operator who rewrote half of it made a different decision from one who
  -- pressed send.
  disposition text NOT NULL CHECK (disposition IN ('accepted', 'edited', 'rejected')),
  -- consequential marks the adverse or financial ones, so an export can be
  -- narrowed to the decisions anybody will actually ask about.
  consequential boolean NOT NULL DEFAULT false,
  summary text CHECK (summary IS NULL OR char_length(summary) <= 500),
  audit_event_id uuid REFERENCES audit_events(id)
);

CREATE INDEX ai_decisions_subject_idx ON ai_decisions (subject_type, subject_id, occurred_at DESC);
CREATE INDEX ai_decisions_consequential_idx ON ai_decisions (occurred_at DESC)
  WHERE consequential;

-- Legal holds on AI material.
--
-- A hold stops retention deletion for one subject. It is a separate table rather
-- than a flag because a hold has a reason, a person, and an end, and a boolean
-- has none of those.
CREATE TABLE ai_retention_holds (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  subject_type text NOT NULL CHECK (char_length(subject_type) BETWEEN 1 AND 60),
  subject_id text NOT NULL CHECK (char_length(subject_id) BETWEEN 1 AND 120),
  reason text NOT NULL CHECK (char_length(reason) BETWEEN 3 AND 500),
  placed_by uuid NOT NULL REFERENCES admin_users(id),
  placed_at timestamptz NOT NULL DEFAULT now(),
  released_at timestamptz,
  released_by uuid REFERENCES admin_users(id),

  CONSTRAINT ai_retention_holds_release_shape
    CHECK ((released_at IS NULL) = (released_by IS NULL))
);

CREATE UNIQUE INDEX ai_retention_holds_active_idx
  ON ai_retention_holds (subject_type, subject_id)
  WHERE released_at IS NULL;

-- MCP servers an owner has registered.
CREATE TABLE mcp_servers (
  slug text PRIMARY KEY CHECK (slug ~ '^[a-z0-9][a-z0-9_-]{1,40}$'),
  display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 120),
  endpoint text NOT NULL CHECK (char_length(endpoint) BETWEEN 8 AND 500),

  enabled boolean NOT NULL DEFAULT false,

  -- Sealed and write-only, like every other credential here.
  credentials_ciphertext bytea,

  -- Egress. allowed_hosts is additional to the endpoint's own host; the empty
  -- array means the endpoint only.
  allowed_hosts text[] NOT NULL DEFAULT '{}',
  allow_private_network boolean NOT NULL DEFAULT false,

  timeout_ms integer NOT NULL DEFAULT 20000 CHECK (timeout_ms BETWEEN 1000 AND 120000),
  max_response_bytes bigint NOT NULL DEFAULT 262144
    CHECK (max_response_bytes BETWEEN 1024 AND 8388608),
  max_calls_per_request integer NOT NULL DEFAULT 8 CHECK (max_calls_per_request BETWEEN 1 AND 100),
  max_depth integer NOT NULL DEFAULT 3 CHECK (max_depth BETWEEN 1 AND 10),
  cost_limit_minor bigint CHECK (cost_limit_minor IS NULL OR cost_limit_minor >= 0),

  -- Discovery results, cached. They are metadata about somebody else's server,
  -- so they are refreshed explicitly and stamped, never assumed current.
  protocol_version text CHECK (protocol_version IS NULL OR char_length(protocol_version) <= 40),
  server_name text CHECK (server_name IS NULL OR char_length(server_name) <= 200),
  server_version text CHECK (server_version IS NULL OR char_length(server_version) <= 60),
  capabilities jsonb NOT NULL DEFAULT '{}'::jsonb,
  discovered_at timestamptz,

  -- Health, so the panel can say "unavailable" instead of showing a tool that
  -- will time out.
  last_checked_at timestamptz,
  last_check_ok boolean,
  last_check_detail text CHECK (last_check_detail IS NULL OR char_length(last_check_detail) <= 500),
  consecutive_failures integer NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),

  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid REFERENCES admin_users(id),

  -- Plaintext is refused in the database as well as in Go. A constraint that
  -- only lives in application code is a constraint one migration script can
  -- bypass.
  CONSTRAINT mcp_servers_requires_tls
    CHECK (endpoint LIKE 'https://%' OR (allow_private_network AND endpoint LIKE 'http://%'))
);

-- The tools an owner has allowlisted, and what each requires.
--
-- A separate table rather than an array column because every row carries a
-- permission and a write flag, and because "which tools are enabled?" is a
-- question the audit and the panel both ask.
CREATE TABLE mcp_tools (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  server_slug text NOT NULL REFERENCES mcp_servers(slug) ON DELETE CASCADE,
  tool_name text NOT NULL CHECK (char_length(tool_name) BETWEEN 1 AND 120),

  enabled boolean NOT NULL DEFAULT false,

  -- The Omniflow permission an operator must hold. Not null: a tool without one
  -- would be reachable by anyone who can reach the assistant.
  permission text NOT NULL CHECK (char_length(permission) BETWEEN 3 AND 60),

  -- Whether the tool changes something outside Omniflow. Owner-maintained rather
  -- than taken from the server's own annotation, because a server that wanted to
  -- avoid a confirmation prompt would simply not set the hint.
  writes boolean NOT NULL DEFAULT true,

  -- The schemas as last discovered, kept so a change is visible as a diff rather
  -- than as behaviour that quietly differs.
  input_schema jsonb,
  output_schema jsonb,
  description text CHECK (description IS NULL OR char_length(description) <= 2000),
  schema_usable boolean NOT NULL DEFAULT false,
  schema_problem text CHECK (schema_problem IS NULL OR char_length(schema_problem) <= 500),

  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),

  UNIQUE (server_slug, tool_name)
);

-- Everything that happened on an MCP connection.
--
-- Refusals are recorded alongside successes, because an audit trail that only
-- records what happened cannot answer "did anyone try?".
CREATE TABLE mcp_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  occurred_at timestamptz NOT NULL DEFAULT now(),

  kind text NOT NULL CHECK (kind IN (
    'connection_changed', 'discovery', 'tool_call', 'confirmation', 'failure'
  )),
  server_slug text CHECK (server_slug IS NULL OR char_length(server_slug) <= 60),
  tool_name text CHECK (tool_name IS NULL OR char_length(tool_name) <= 120),
  operator_id uuid REFERENCES admin_users(id) ON DELETE SET NULL,

  -- The arguments as sent. They are the tool's own arguments, which an operator
  -- has already seen in the preview, and they are what makes a later question
  -- about a call answerable.
  arguments jsonb NOT NULL DEFAULT '{}'::jsonb,
  confirmed boolean NOT NULL DEFAULT false,
  reason text CHECK (reason IS NULL OR char_length(reason) <= 500),

  outcome text NOT NULL CHECK (outcome IN ('allowed', 'refused', 'failed', 'replayed')),
  detail text CHECK (detail IS NULL OR char_length(detail) <= 1000),
  response_bytes bigint NOT NULL DEFAULT 0 CHECK (response_bytes >= 0),
  duration_ms integer NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),

  -- Injection patterns detected in the result, so an answer that turns out
  -- strange has a record of the material that produced it.
  findings text[] NOT NULL DEFAULT '{}'
);

CREATE INDEX mcp_events_recent_idx ON mcp_events (occurred_at DESC);
CREATE INDEX mcp_events_server_idx ON mcp_events (server_slug, occurred_at DESC)
  WHERE server_slug IS NOT NULL;

-- The features exist as rows from the start, all off.
--
-- Seeding them disabled rather than leaving the table empty means the settings
-- screen renders the full list of what an installation could enable, and the
-- absence of a row never has to mean "off" in application code.
INSERT INTO ai_features (feature) VALUES
  ('support_summary'), ('support_reply'), ('support_rewrite'), ('support_translate'),
  ('support_classify'), ('marketing_draft'), ('risk_analysis'), ('copilot'), ('mcp_tools');

-- Settings sections likewise, empty and unversioned until an owner saves one.
INSERT INTO installation_settings (section) VALUES
  ('branding'), ('remnawave'), ('telegram'), ('operator_group'), ('required_channels'),
  ('maintenance'), ('notifications'), ('telemetry'), ('backup'), ('security'), ('ai'), ('mcp');
