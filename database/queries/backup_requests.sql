-- Backups asked for in the web panel and carried out by the worker.

-- name: CreateBackupRequest :one
INSERT INTO backup_requests (action, backup_id, reason, requested_by)
VALUES (sqlc.arg(action), sqlc.narg(backup_id), sqlc.arg(reason), sqlc.narg(requested_by))
RETURNING *;

-- name: ClaimBackupRequest :one
UPDATE backup_requests SET status = 'running'
WHERE id = (SELECT id FROM backup_requests WHERE status = 'pending' ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED)
RETURNING *;

-- name: FinishBackupRequest :exec
UPDATE backup_requests SET status = sqlc.arg(status), error_code = sqlc.narg(error_code), finished_at = now()
WHERE id = sqlc.arg(request_id);

-- name: ListBackupRequests :many
SELECT * FROM backup_requests ORDER BY created_at DESC LIMIT 20;

-- name: RecordUploadedBackup :one
INSERT INTO backups (kind, status, file_name, encrypted, requested_by, size_bytes, sha256, completed_at, verified_at, retain_until)
VALUES ('uploaded', 'completed', sqlc.arg(file_name), true, sqlc.narg(requested_by), sqlc.arg(size_bytes), sqlc.arg(sha256), now(), now(), sqlc.arg(retain_until))
RETURNING *;
