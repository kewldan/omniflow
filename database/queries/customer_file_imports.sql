-- Customer imports staged from an uploaded file.

-- name: CreateFileCustomerImport :one
-- An import staged from an uploaded CSV rather than read from Remnawave.
INSERT INTO customer_imports (source, status) VALUES ('file', 'previewing') RETURNING *;
