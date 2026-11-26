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

-- Downloads: store links and mirrored installers for one client.
--
-- A mirror is offered to customers only once the worker has copied a file for
-- it; until then it is a source with nothing to serve.

-- name: ListEnabledConnectDownloads :many
SELECT d.id, d.client_id, d.kind, d.label, d.url, d.version, d.file_name, d.size_bytes
FROM connect_downloads d
JOIN connect_clients c ON c.id = d.client_id
WHERE d.enabled
  AND c.enabled
  AND c.platform_slug = $1
  AND (d.kind = 'store' OR d.checksum IS NOT NULL)
ORDER BY d.sort_order, d.label;

-- name: ListConnectDownloads :many
SELECT id, client_id, kind, label, url, source_repo, tag_prefix, asset_pattern,
       version, file_name, size_bytes, checksum, fetched_at, next_check_at,
       checked_at, last_error, enabled, sort_order, updated_at, updated_by
FROM connect_downloads
ORDER BY client_id, sort_order, label;

-- name: UpsertConnectDownload :one
-- Changing a mirror's source, or switching it back on, makes it due at once,
-- so the screen does not show a file from the previous source for hours. A
-- row turned into a store link drops the file it held; the sweep reclaims it.
INSERT INTO connect_downloads (
  id, client_id, kind, label, url, source_repo, tag_prefix, asset_pattern,
  enabled, sort_order, updated_by
)
VALUES (
  COALESCE(sqlc.narg(id)::uuid, gen_random_uuid()), $1, $2, $3,
  sqlc.narg(url), sqlc.narg(source_repo), $4, sqlc.narg(asset_pattern),
  $5, $6, sqlc.narg(updated_by)
)
ON CONFLICT (id) DO UPDATE
SET client_id = EXCLUDED.client_id,
    kind = EXCLUDED.kind,
    label = EXCLUDED.label,
    url = EXCLUDED.url,
    source_repo = EXCLUDED.source_repo,
    tag_prefix = EXCLUDED.tag_prefix,
    asset_pattern = EXCLUDED.asset_pattern,
    enabled = EXCLUDED.enabled,
    sort_order = EXCLUDED.sort_order,
    next_check_at = CASE
      WHEN (connect_downloads.source_repo, connect_downloads.tag_prefix, connect_downloads.asset_pattern)
           IS DISTINCT FROM (EXCLUDED.source_repo, EXCLUDED.tag_prefix, EXCLUDED.asset_pattern)
        OR (EXCLUDED.enabled AND NOT connect_downloads.enabled)
      THEN now()
      ELSE connect_downloads.next_check_at
    END,
    version = CASE WHEN EXCLUDED.kind = 'mirror' THEN connect_downloads.version END,
    file_name = CASE WHEN EXCLUDED.kind = 'mirror' THEN connect_downloads.file_name END,
    size_bytes = CASE WHEN EXCLUDED.kind = 'mirror' THEN connect_downloads.size_bytes END,
    checksum = CASE WHEN EXCLUDED.kind = 'mirror' THEN connect_downloads.checksum END,
    content_type = CASE WHEN EXCLUDED.kind = 'mirror' THEN connect_downloads.content_type END,
    fetched_at = CASE WHEN EXCLUDED.kind = 'mirror' THEN connect_downloads.fetched_at END,
    last_error = CASE WHEN EXCLUDED.kind = 'mirror' THEN connect_downloads.last_error END,
    updated_at = now(),
    updated_by = EXCLUDED.updated_by
RETURNING id, client_id, kind, label, url, source_repo, tag_prefix, asset_pattern,
          version, file_name, size_bytes, checksum, fetched_at, next_check_at,
          checked_at, last_error, enabled, sort_order, updated_at, updated_by;

-- name: DeleteConnectDownload :execrows
DELETE FROM connect_downloads WHERE id = $1;

-- name: RequestConnectMirrorCheck :execrows
UPDATE connect_downloads SET next_check_at = now()
WHERE id = $1 AND kind = 'mirror';

-- name: DueConnectMirrors :many
SELECT id, source_repo, tag_prefix, asset_pattern, checksum, version, file_name, size_bytes
FROM connect_downloads
WHERE kind = 'mirror' AND enabled AND next_check_at <= now()
ORDER BY next_check_at
LIMIT $1;

-- name: RecordConnectMirrorFile :execrows
-- The source is part of the condition: an operator who changed it while the
-- previous one was downloading must not end up with the previous file.
UPDATE connect_downloads
SET version = sqlc.arg(version),
    file_name = sqlc.arg(file_name),
    size_bytes = sqlc.arg(size_bytes),
    checksum = sqlc.arg(checksum),
    content_type = sqlc.arg(content_type),
    fetched_at = now(),
    checked_at = now(),
    last_error = NULL,
    next_check_at = sqlc.arg(next_check_at)
WHERE id = sqlc.arg(id)
  AND kind = 'mirror'
  AND source_repo = sqlc.arg(source_repo)
  AND tag_prefix = sqlc.arg(tag_prefix)
  AND asset_pattern = sqlc.arg(asset_pattern);

-- name: RecordConnectMirrorChecked :exec
UPDATE connect_downloads
SET checked_at = now(), last_error = sqlc.narg(last_error), next_check_at = sqlc.arg(next_check_at)
WHERE id = sqlc.arg(id) AND kind = 'mirror';

-- name: ConnectDownloadFile :one
-- What the public download address serves. A file behind a switched-off
-- download, client, or platform is not served, as it is not listed.
SELECT d.file_name, d.size_bytes, d.checksum, d.content_type, d.fetched_at
FROM connect_downloads d
JOIN connect_clients c ON c.id = d.client_id
JOIN connect_platforms p ON p.slug = c.platform_slug
WHERE d.id = $1
  AND d.kind = 'mirror'
  AND d.checksum IS NOT NULL
  AND d.enabled AND c.enabled AND p.enabled;

-- name: ListConnectDownloadChecksums :many
SELECT DISTINCT checksum::text FROM connect_downloads WHERE checksum IS NOT NULL;
