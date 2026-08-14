-- Connection guidance.
--
-- The customer-facing reads select only enabled rows, so disabling a platform
-- or a client removes it from the bot and the browser at the same moment. The
-- operator reads select everything, because a disabled row is exactly what an
-- operator came to the screen to re-enable.

-- name: ListEnabledConnectPlatforms :many
SELECT slug, label_en, label_ru
FROM connect_platforms
WHERE enabled
ORDER BY sort_order, slug;

-- name: ListEnabledConnectClients :many
SELECT id, platform_slug, name, scheme, download_url, instructions_en, instructions_ru
FROM connect_clients
WHERE enabled
  AND platform_slug = $1
  -- A client on a platform somebody disabled must not be reachable by naming
  -- the platform directly, so the join carries the platform's own flag.
  AND EXISTS (
    SELECT 1 FROM connect_platforms
    WHERE connect_platforms.slug = connect_clients.platform_slug
      AND connect_platforms.enabled
  )
ORDER BY sort_order, name;

-- name: ListConnectPlatforms :many
SELECT slug, label_en, label_ru, enabled, sort_order, updated_at, updated_by
FROM connect_platforms
ORDER BY sort_order, slug;

-- name: ListConnectClients :many
SELECT id, platform_slug, name, scheme, download_url, instructions_en, instructions_ru,
       enabled, sort_order, updated_at, updated_by
FROM connect_clients
ORDER BY platform_slug, sort_order, name;

-- name: UpsertConnectPlatform :one
INSERT INTO connect_platforms (slug, label_en, label_ru, enabled, sort_order, updated_by)
VALUES ($1, $2, $3, $4, $5, sqlc.narg(updated_by))
ON CONFLICT (slug) DO UPDATE
SET label_en = EXCLUDED.label_en,
    label_ru = EXCLUDED.label_ru,
    enabled = EXCLUDED.enabled,
    sort_order = EXCLUDED.sort_order,
    updated_at = now(),
    updated_by = EXCLUDED.updated_by
RETURNING slug, label_en, label_ru, enabled, sort_order, updated_at, updated_by;

-- name: DeleteConnectPlatform :execrows
DELETE FROM connect_platforms WHERE slug = $1;

-- name: UpsertConnectClient :one
-- The identifier is optional: absent creates, present updates. The pairing of
-- platform and name is unique, so a rename that collides is refused by the
-- table rather than silently producing two rows the screen shows twice.
INSERT INTO connect_clients (
  id, platform_slug, name, scheme, download_url,
  instructions_en, instructions_ru, enabled, sort_order, updated_by
)
VALUES (
  COALESCE(sqlc.narg(id)::uuid, gen_random_uuid()), $1, $2, $3,
  sqlc.narg(download_url), sqlc.narg(instructions_en), sqlc.narg(instructions_ru),
  $4, $5, sqlc.narg(updated_by)
)
ON CONFLICT (id) DO UPDATE
SET platform_slug = EXCLUDED.platform_slug,
    name = EXCLUDED.name,
    scheme = EXCLUDED.scheme,
    download_url = EXCLUDED.download_url,
    instructions_en = EXCLUDED.instructions_en,
    instructions_ru = EXCLUDED.instructions_ru,
    enabled = EXCLUDED.enabled,
    sort_order = EXCLUDED.sort_order,
    updated_at = now(),
    updated_by = EXCLUDED.updated_by
RETURNING id, platform_slug, name, scheme, download_url, instructions_en, instructions_ru,
          enabled, sort_order, updated_at, updated_by;

-- name: DeleteConnectClient :execrows
DELETE FROM connect_clients WHERE id = $1;
