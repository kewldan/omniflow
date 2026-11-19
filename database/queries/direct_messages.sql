-- name: InsertCustomerDirectMessage :one
INSERT INTO customer_direct_messages (user_id, body, created_by)
VALUES (sqlc.arg(user_id), sqlc.arg(body), sqlc.narg(created_by))
RETURNING id;

-- name: ListCustomerDirectMessages :many
SELECT id, body, status, created_at, sent_at, error_code
FROM customer_direct_messages WHERE user_id = sqlc.arg(user_id)
ORDER BY created_at DESC LIMIT 20;
