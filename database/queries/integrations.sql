-- Named API tokens and outgoing webhooks.

-- name: CreateAPIToken :one
INSERT INTO api_tokens (name, token_hash, prefix, created_by, permissions)
VALUES (sqlc.arg(name), sqlc.arg(token_hash), sqlc.arg(prefix), sqlc.narg(created_by), sqlc.narg(permissions)::text[])
RETURNING *;

-- name: ListAPITokens :many
SELECT * FROM api_tokens ORDER BY created_at DESC;

-- name: RevokeAPIToken :execrows
UPDATE api_tokens SET revoked_at = now() WHERE id = sqlc.arg(token_id) AND revoked_at IS NULL;

-- name: ResolveAPIToken :one
UPDATE api_tokens SET last_used_at = now()
WHERE token_hash = sqlc.arg(token_hash) AND revoked_at IS NULL
RETURNING id, name, permissions, created_by;

-- name: LogAPITokenRequest :exec
INSERT INTO api_token_requests (token_id, method, route, status)
VALUES (sqlc.arg(token_id), sqlc.arg(method), sqlc.arg(route), sqlc.arg(status));

-- name: ListAPITokenRequests :many
SELECT * FROM api_token_requests WHERE token_id = sqlc.arg(token_id) ORDER BY occurred_at DESC LIMIT 100;

-- name: PruneAPITokenRequests :execrows
DELETE FROM api_token_requests WHERE occurred_at < now() - interval '90 days';

-- name: CreateWebhookEndpoint :one
INSERT INTO webhook_endpoints (name, url, events, secret_ciphertext, created_by)
VALUES (sqlc.arg(name), sqlc.arg(url), sqlc.arg(events), sqlc.arg(secret_ciphertext), sqlc.narg(created_by))
RETURNING *;

-- name: ListWebhookEndpoints :many
SELECT id, name, url, events, enabled, created_at, updated_at,
  (SELECT count(*) FROM webhook_deliveries d WHERE d.endpoint_id = e.id AND d.status = 'failed')::bigint AS failed_count,
  (SELECT max(d.delivered_at) FROM webhook_deliveries d WHERE d.endpoint_id = e.id)::timestamptz AS last_delivered_at
FROM webhook_endpoints e ORDER BY created_at DESC;

-- name: SetWebhookEndpointEnabled :execrows
UPDATE webhook_endpoints SET enabled = sqlc.arg(enabled), updated_at = now() WHERE id = sqlc.arg(endpoint_id);

-- name: DeleteWebhookEndpoint :execrows
DELETE FROM webhook_endpoints WHERE id = sqlc.arg(endpoint_id);

-- name: ListWebhookDeliveries :many
SELECT * FROM webhook_deliveries WHERE endpoint_id = sqlc.arg(endpoint_id) ORDER BY created_at DESC LIMIT 100;

-- name: FanOutOutboxEvents :execrows
-- Each unpublished outbox event becomes one delivery per enabled endpoint
-- subscribed to its topic, and is marked published in the same statement.
WITH events AS (
  SELECT id, topic, payload, occurred_at FROM outbox_events
  WHERE published_at IS NULL
  ORDER BY occurred_at
  LIMIT 200
  FOR UPDATE SKIP LOCKED
), deliveries AS (
  INSERT INTO webhook_deliveries (endpoint_id, event_id, topic, payload)
  SELECT w.id, e.id, e.topic,
         jsonb_build_object('id', e.id::text, 'topic', e.topic, 'occurredAt', e.occurred_at, 'data', e.payload)
  FROM events e
  JOIN webhook_endpoints w ON w.enabled AND e.topic = ANY (w.events)
  ON CONFLICT (endpoint_id, event_id) DO NOTHING
)
UPDATE outbox_events o SET published_at = now() FROM events WHERE o.id = events.id;

-- name: DueWebhookDeliveries :many
SELECT d.id, d.payload, d.attempts, w.url, w.secret_ciphertext, w.id AS endpoint_id
FROM webhook_deliveries d
JOIN webhook_endpoints w ON w.id = d.endpoint_id
WHERE d.status = 'pending' AND d.next_attempt_at <= now() AND w.enabled
ORDER BY d.next_attempt_at
LIMIT 50
FOR UPDATE OF d SKIP LOCKED;

-- name: RecordWebhookAttempt :exec
UPDATE webhook_deliveries
SET attempts = attempts + 1, response_status = sqlc.narg(response_status), status = sqlc.arg(status),
    next_attempt_at = sqlc.arg(next_attempt_at),
    delivered_at = CASE WHEN sqlc.arg(status)::text = 'delivered' THEN now() ELSE delivered_at END
WHERE id = sqlc.arg(delivery_id);
