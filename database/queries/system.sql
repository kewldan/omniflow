-- name: GetTelemetryInstallationID :one
SELECT installation_id FROM telemetry_installation WHERE singleton = true;

-- name: InsertOutboxEvent :one
INSERT INTO outbox_events (topic, payload) VALUES ($1, $2) RETURNING *;
