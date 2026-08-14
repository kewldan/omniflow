-- Brand images.
--
-- The bytes are selected only by the route that serves them. Every other query
-- reads the metadata, so listing the slots on a settings screen cannot pull a
-- quarter of a megabyte per row into a response type that has nowhere to put it.

-- name: ListBrandingAssets :many
SELECT kind, content_type, octet_length(bytes)::bigint AS byte_size, checksum,
       updated_at, updated_by
FROM branding_assets
ORDER BY kind;

-- name: GetBrandingAsset :one
SELECT kind, content_type, bytes, checksum, updated_at
FROM branding_assets
WHERE kind = $1;

-- name: UpsertBrandingAsset :one
INSERT INTO branding_assets (kind, content_type, bytes, checksum, updated_by)
VALUES ($1, $2, $3, $4, sqlc.narg(updated_by))
ON CONFLICT (kind) DO UPDATE
SET content_type = EXCLUDED.content_type,
    bytes = EXCLUDED.bytes,
    checksum = EXCLUDED.checksum,
    updated_at = now(),
    updated_by = EXCLUDED.updated_by
RETURNING kind, content_type, octet_length(bytes)::bigint AS byte_size, checksum,
          updated_at, updated_by;

-- name: DeleteBrandingAsset :execrows
DELETE FROM branding_assets WHERE kind = $1;
