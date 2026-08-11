-- AI governance, MCP connections, and installation settings.

-- name: ListSettingSections :many
-- Every section without its secrets. This is the query the settings screens
-- read, and it cannot return a credential because the column is not selected.
SELECT section, document, version, updated_at, updated_by
FROM installation_settings
ORDER BY section;

-- name: GetSettingSection :one
SELECT section, document, version, updated_at, updated_by
FROM installation_settings
WHERE section = $1;

-- name: SaveSettingSection :one
-- The version guard turns two panel tabs saving the same screen into a conflict
-- rather than a silent overwrite. A caller that gets no row re-reads and retries.
UPDATE installation_settings
SET document = sqlc.arg(document),
    version = version + 1,
    updated_at = now(),
    updated_by = sqlc.narg(updated_by)
WHERE section = sqlc.arg(section)
  AND version = sqlc.arg(expected_version)
RETURNING section, document, version, updated_at, updated_by;

-- name: SaveSettingSecrets :exec
-- Secrets are written on their own so a screen that did not change a token does
-- not have to re-send it, which is what makes a write-only field workable.
UPDATE installation_settings
SET secrets_ciphertext = sqlc.arg(secrets_ciphertext),
    updated_at = now(),
    updated_by = sqlc.narg(updated_by)
WHERE section = sqlc.arg(section);

-- name: GetSettingSecrets :one
-- Read by the processes that need the credential, never by a request handler
-- that answers an operator.
SELECT secrets_ciphertext FROM installation_settings WHERE section = $1;

-- name: SettingSecretsPresent :many
-- Whether a secret exists, without returning it. It is what the panel shows in
-- place of the value: "configured" is the only safe rendering of a secret.
SELECT section, secrets_ciphertext IS NOT NULL AS configured
FROM installation_settings
ORDER BY section;

-- name: ListAIProviders :many
SELECT slug, kind, display_name, base_url, enabled, zero_retention, trains_on_data,
       retention_notice, data_region, last_checked_at, last_check_ok, last_check_detail,
       credentials_ciphertext IS NOT NULL AS credential_configured,
       created_at, updated_at, updated_by
FROM ai_providers
ORDER BY display_name;

-- name: GetAIProviderCredentials :one
SELECT kind, base_url, credentials_ciphertext FROM ai_providers WHERE slug = $1 AND enabled;

-- name: UpsertAIProvider :one
INSERT INTO ai_providers (
  slug, kind, display_name, base_url, enabled, zero_retention, trains_on_data,
  retention_notice, data_region, updated_by
) VALUES (
  sqlc.arg(slug), sqlc.arg(kind), sqlc.arg(display_name), sqlc.narg(base_url),
  sqlc.arg(enabled), sqlc.arg(zero_retention), sqlc.arg(trains_on_data),
  sqlc.narg(retention_notice), sqlc.narg(data_region), sqlc.narg(updated_by)
)
ON CONFLICT (slug) DO UPDATE SET
  kind = EXCLUDED.kind, display_name = EXCLUDED.display_name,
  base_url = EXCLUDED.base_url, enabled = EXCLUDED.enabled,
  zero_retention = EXCLUDED.zero_retention, trains_on_data = EXCLUDED.trains_on_data,
  retention_notice = EXCLUDED.retention_notice, data_region = EXCLUDED.data_region,
  updated_at = now(), updated_by = EXCLUDED.updated_by
RETURNING slug, kind, display_name, base_url, enabled, zero_retention, trains_on_data,
          retention_notice, data_region, created_at, updated_at;

-- name: SetAIProviderCredentials :exec
UPDATE ai_providers
SET credentials_ciphertext = sqlc.arg(credentials_ciphertext),
    updated_at = now(), updated_by = sqlc.narg(updated_by)
WHERE slug = sqlc.arg(slug);

-- name: RecordAIProviderCheck :exec
UPDATE ai_providers
SET last_checked_at = now(), last_check_ok = sqlc.arg(ok),
    last_check_detail = sqlc.narg(detail)
WHERE slug = sqlc.arg(slug);

-- name: DeleteAIProvider :exec
DELETE FROM ai_providers WHERE slug = $1;

-- name: ListAIFeatures :many
SELECT * FROM ai_features ORDER BY feature;

-- name: GetAIFeature :one
SELECT * FROM ai_features WHERE feature = $1;

-- name: ConfigureAIFeature :one
UPDATE ai_features
SET enabled = sqlc.arg(enabled),
    provider_slug = sqlc.narg(provider_slug),
    model = sqlc.narg(model),
    temperature = sqlc.narg(temperature),
    max_tokens = sqlc.narg(max_tokens),
    timeout_ms = sqlc.narg(timeout_ms),
    budget_tokens = sqlc.narg(budget_tokens),
    budget_window_seconds = sqlc.narg(budget_window_seconds),
    budget_cost_minor = sqlc.narg(budget_cost_minor),
    retain_prompts = sqlc.arg(retain_prompts),
    retain_outputs = sqlc.arg(retain_outputs),
    retention_days = sqlc.arg(retention_days),
    updated_at = now(),
    updated_by = sqlc.narg(updated_by)
WHERE feature = sqlc.arg(feature)
RETURNING *;

-- name: ListAIUsageLimits :many
SELECT * FROM ai_usage_limits ORDER BY scope, coalesce(scope_ref, ''), coalesce(feature, '');

-- name: UpsertAIUsageLimit :one
INSERT INTO ai_usage_limits (
  scope, scope_ref, feature, window_seconds, max_requests, max_tokens,
  max_cost_minor, updated_by
) VALUES (
  sqlc.arg(scope), sqlc.narg(scope_ref), sqlc.narg(feature), sqlc.arg(window_seconds),
  sqlc.narg(max_requests), sqlc.narg(max_tokens), sqlc.narg(max_cost_minor),
  sqlc.narg(updated_by)
)
ON CONFLICT (scope, coalesce(scope_ref, ''), coalesce(feature, '')) DO UPDATE SET
  window_seconds = EXCLUDED.window_seconds,
  max_requests = EXCLUDED.max_requests,
  max_tokens = EXCLUDED.max_tokens,
  max_cost_minor = EXCLUDED.max_cost_minor,
  updated_at = now(), updated_by = EXCLUDED.updated_by
RETURNING *;

-- name: DeleteAIUsageLimit :exec
DELETE FROM ai_usage_limits WHERE id = $1;

-- name: RecordAIUsage :exec
-- There is no prompt or output parameter, and there is nowhere to put one.
INSERT INTO ai_usage_events (
  feature, task, provider_slug, model, operator_id, operator_role,
  input_tokens, output_tokens, latency_ms, estimated_cost_minor, currency,
  outcome, error_code, redaction_summary
) VALUES (
  sqlc.arg(feature), sqlc.arg(task), sqlc.arg(provider_slug), sqlc.arg(model),
  sqlc.narg(operator_id), sqlc.narg(operator_role),
  sqlc.arg(input_tokens), sqlc.arg(output_tokens), sqlc.arg(latency_ms),
  sqlc.arg(estimated_cost_minor), sqlc.narg(currency),
  sqlc.arg(outcome), sqlc.narg(error_code), sqlc.arg(redaction_summary)
);

-- name: AIUsageForWindow :one
-- What one scope has spent, for the budget check that runs before a call.
SELECT
  count(*)::bigint AS requests,
  coalesce(sum(input_tokens + output_tokens), 0)::bigint AS tokens,
  coalesce(sum(estimated_cost_minor), 0)::bigint AS cost_minor
FROM ai_usage_events
WHERE occurred_at >= now() - make_interval(secs => sqlc.arg(window_seconds)::double precision)
  AND (sqlc.narg(feature)::text IS NULL OR feature = sqlc.narg(feature)::text)
  AND (sqlc.narg(operator_id)::uuid IS NULL OR operator_id = sqlc.narg(operator_id)::uuid)
  AND (sqlc.narg(operator_role)::text IS NULL OR operator_role = sqlc.narg(operator_role)::text);

-- name: AIUsageReport :many
-- Token, request, latency, error, and estimated-cost reporting, grouped by the
-- dimensions an owner reads. No prompt content appears because none is stored.
SELECT
  feature,
  provider_slug,
  model,
  count(*)::bigint AS requests,
  coalesce(sum(input_tokens), 0)::bigint AS input_tokens,
  coalesce(sum(output_tokens), 0)::bigint AS output_tokens,
  coalesce(sum(estimated_cost_minor), 0)::bigint AS cost_minor,
  coalesce(round(avg(latency_ms))::bigint, 0) AS mean_latency_ms,
  coalesce(percentile_disc(0.95) WITHIN GROUP (ORDER BY latency_ms), 0)::bigint AS p95_latency_ms,
  count(*) FILTER (WHERE outcome <> 'succeeded')::bigint AS failures
FROM ai_usage_events
WHERE occurred_at >= sqlc.arg(since) AND occurred_at < sqlc.arg(until)
GROUP BY feature, provider_slug, model
ORDER BY feature, provider_slug, model;

-- name: PurgeAIUsageBefore :execrows
DELETE FROM ai_usage_events WHERE occurred_at < $1;

-- name: RecordAIDecision :one
INSERT INTO ai_decisions (
  subject_type, subject_id, feature, provider_slug, model, policy_version,
  operator_id, disposition, consequential, summary, audit_event_id
) VALUES (
  sqlc.arg(subject_type), sqlc.arg(subject_id), sqlc.arg(feature),
  sqlc.arg(provider_slug), sqlc.arg(model), sqlc.narg(policy_version),
  sqlc.arg(operator_id), sqlc.arg(disposition), sqlc.arg(consequential),
  sqlc.narg(summary), sqlc.narg(audit_event_id)
)
RETURNING *;

-- name: ExportAIDecisions :many
-- The export that answers "which of these were shaped by a model?".
SELECT d.*, a.email AS operator_email
FROM ai_decisions d
JOIN admin_users a ON a.id = d.operator_id
WHERE d.occurred_at >= sqlc.arg(since) AND d.occurred_at < sqlc.arg(until)
  AND (NOT sqlc.arg(consequential_only)::boolean OR d.consequential)
ORDER BY d.occurred_at DESC, d.id DESC
LIMIT sqlc.arg(page_size);

-- name: PlaceRetentionHold :one
INSERT INTO ai_retention_holds (subject_type, subject_id, reason, placed_by)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: ReleaseRetentionHold :one
UPDATE ai_retention_holds
SET released_at = now(), released_by = sqlc.arg(released_by)
WHERE id = sqlc.arg(id) AND released_at IS NULL
RETURNING *;

-- name: ListActiveRetentionHolds :many
SELECT * FROM ai_retention_holds WHERE released_at IS NULL ORDER BY placed_at DESC;

-- name: ListMCPServers :many
-- The registry as the panel reads it. The credential column is not selected, so
-- listing servers cannot leak one.
SELECT slug, display_name, endpoint, enabled, allowed_hosts, allow_private_network,
       timeout_ms, max_response_bytes, max_calls_per_request, max_depth, cost_limit_minor,
       protocol_version, server_name, server_version, capabilities, discovered_at,
       last_checked_at, last_check_ok, last_check_detail, consecutive_failures,
       credentials_ciphertext IS NOT NULL AS credential_configured,
       created_at, updated_at, updated_by
FROM mcp_servers
ORDER BY display_name;

-- name: GetMCPServer :one
SELECT slug, display_name, endpoint, enabled, allowed_hosts, allow_private_network,
       timeout_ms, max_response_bytes, max_calls_per_request, max_depth, cost_limit_minor,
       protocol_version, server_name, server_version, capabilities, discovered_at,
       last_checked_at, last_check_ok, last_check_detail, consecutive_failures,
       credentials_ciphertext IS NOT NULL AS credential_configured,
       created_at, updated_at, updated_by
FROM mcp_servers
WHERE slug = $1;

-- name: GetMCPServerCredentials :one
SELECT credentials_ciphertext FROM mcp_servers WHERE slug = $1 AND enabled;

-- name: UpsertMCPServer :one
INSERT INTO mcp_servers (
  slug, display_name, endpoint, enabled, allowed_hosts, allow_private_network,
  timeout_ms, max_response_bytes, max_calls_per_request, max_depth, cost_limit_minor,
  updated_by
) VALUES (
  sqlc.arg(slug), sqlc.arg(display_name), sqlc.arg(endpoint), sqlc.arg(enabled),
  sqlc.arg(allowed_hosts), sqlc.arg(allow_private_network), sqlc.arg(timeout_ms),
  sqlc.arg(max_response_bytes), sqlc.arg(max_calls_per_request), sqlc.arg(max_depth),
  sqlc.narg(cost_limit_minor), sqlc.narg(updated_by)
)
ON CONFLICT (slug) DO UPDATE SET
  display_name = EXCLUDED.display_name, endpoint = EXCLUDED.endpoint,
  enabled = EXCLUDED.enabled, allowed_hosts = EXCLUDED.allowed_hosts,
  allow_private_network = EXCLUDED.allow_private_network,
  timeout_ms = EXCLUDED.timeout_ms, max_response_bytes = EXCLUDED.max_response_bytes,
  max_calls_per_request = EXCLUDED.max_calls_per_request, max_depth = EXCLUDED.max_depth,
  cost_limit_minor = EXCLUDED.cost_limit_minor,
  updated_at = now(), updated_by = EXCLUDED.updated_by
RETURNING slug, display_name, endpoint, enabled, allowed_hosts, allow_private_network,
          timeout_ms, max_response_bytes, max_calls_per_request, max_depth,
          cost_limit_minor, created_at, updated_at;

-- name: SetMCPServerCredentials :exec
UPDATE mcp_servers
SET credentials_ciphertext = sqlc.arg(credentials_ciphertext),
    updated_at = now(), updated_by = sqlc.narg(updated_by)
WHERE slug = sqlc.arg(slug);

-- name: DeleteMCPServer :exec
DELETE FROM mcp_servers WHERE slug = $1;

-- name: RecordMCPDiscovery :exec
UPDATE mcp_servers
SET protocol_version = sqlc.narg(protocol_version),
    server_name = sqlc.narg(server_name),
    server_version = sqlc.narg(server_version),
    capabilities = sqlc.arg(capabilities),
    discovered_at = now(),
    last_checked_at = now(),
    last_check_ok = true,
    last_check_detail = NULL,
    consecutive_failures = 0
WHERE slug = sqlc.arg(slug);

-- name: RecordMCPHealth :exec
-- A failure increments; a success resets. The counter is what the panel reads to
-- explain why a connection is being skipped.
UPDATE mcp_servers
SET last_checked_at = now(),
    last_check_ok = sqlc.arg(ok),
    last_check_detail = sqlc.narg(detail),
    consecutive_failures = CASE WHEN sqlc.arg(ok)::boolean THEN 0
                                ELSE consecutive_failures + 1 END
WHERE slug = sqlc.arg(slug);

-- name: ListMCPTools :many
SELECT * FROM mcp_tools WHERE server_slug = $1 ORDER BY tool_name;

-- name: ListEnabledMCPTools :many
-- What an installation actually exposes: enabled tools on enabled servers whose
-- schema this build can enforce.
SELECT t.* FROM mcp_tools t
JOIN mcp_servers s ON s.slug = t.server_slug
WHERE s.enabled AND t.enabled AND t.schema_usable
ORDER BY t.server_slug, t.tool_name;

-- name: RecordDiscoveredMCPTool :one
-- Discovery refreshes the description and schemas and leaves the owner's
-- decisions alone. A rediscovery that re-enabled a tool an owner switched off
-- would make discovery a privilege escalation.
INSERT INTO mcp_tools (
  server_slug, tool_name, permission, writes, input_schema, output_schema,
  description, schema_usable, schema_problem
) VALUES (
  sqlc.arg(server_slug), sqlc.arg(tool_name), sqlc.arg(permission), sqlc.arg(writes),
  sqlc.narg(input_schema), sqlc.narg(output_schema), sqlc.narg(description),
  sqlc.arg(schema_usable), sqlc.narg(schema_problem)
)
ON CONFLICT (server_slug, tool_name) DO UPDATE SET
  input_schema = EXCLUDED.input_schema,
  output_schema = EXCLUDED.output_schema,
  description = EXCLUDED.description,
  schema_usable = EXCLUDED.schema_usable,
  schema_problem = EXCLUDED.schema_problem,
  updated_at = now()
RETURNING *;

-- name: SetMCPToolPolicy :one
-- The owner's decisions: enablement, the permission it maps to, and whether it
-- writes. Separate from discovery for the reason above.
UPDATE mcp_tools
SET enabled = sqlc.arg(enabled),
    permission = sqlc.arg(permission),
    writes = sqlc.arg(writes),
    updated_at = now()
WHERE server_slug = sqlc.arg(server_slug) AND tool_name = sqlc.arg(tool_name)
RETURNING *;

-- name: RecordMCPEvent :exec
INSERT INTO mcp_events (
  kind, server_slug, tool_name, operator_id, arguments, confirmed, reason,
  outcome, detail, response_bytes, duration_ms, findings
) VALUES (
  sqlc.arg(kind), sqlc.narg(server_slug), sqlc.narg(tool_name), sqlc.narg(operator_id),
  sqlc.arg(arguments), sqlc.arg(confirmed), sqlc.narg(reason), sqlc.arg(outcome),
  sqlc.narg(detail), sqlc.arg(response_bytes), sqlc.arg(duration_ms), sqlc.arg(findings)
);

-- name: ListMCPEvents :many
SELECT * FROM mcp_events
WHERE (sqlc.narg(server_slug)::text IS NULL OR server_slug = sqlc.narg(server_slug)::text)
  AND (sqlc.narg(outcome)::text IS NULL OR outcome = sqlc.narg(outcome)::text)
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL
       OR (occurred_at, id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY occurred_at DESC, id DESC
LIMIT sqlc.arg(page_size);
