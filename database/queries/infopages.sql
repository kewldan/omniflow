-- Information pages.
--
-- The public reads select only published pages, so unpublishing removes an
-- address from the world without deleting it — and deleting is the irreversible
-- one, because it takes the address a payment provider approved with it.

-- name: ListPublishedInfoPages :many
-- What the customer panel lists. A page can be published without being listed:
-- a document that exists to satisfy a provider's review needs a stable address
-- and not necessarily a menu entry.
SELECT p.slug, p.kind, p.sort_order, p.updated_at,
       l.locale, l.title
FROM info_pages p
JOIN info_page_localizations l ON l.page_slug = p.slug
WHERE p.published_at IS NOT NULL
  AND p.listed
  AND l.locale = sqlc.arg(locale)
ORDER BY p.sort_order, p.slug;

-- name: GetPublishedInfoPage :one
-- One page by its address, in the reader's language.
--
-- The locale falls back to the other one in the caller rather than here: a page
-- that exists in English only must still answer at its address for a Russian
-- reader, because the address is what a provider approved.
SELECT p.slug, p.kind, p.published_at, p.updated_at,
       l.locale, l.title, l.body
FROM info_pages p
JOIN info_page_localizations l ON l.page_slug = p.slug
WHERE p.slug = sqlc.arg(slug)
  AND p.published_at IS NOT NULL
ORDER BY (l.locale = sqlc.arg(locale)) DESC, l.locale
LIMIT 1;

-- name: ListInfoPages :many
-- The operator's view: every page, including drafts.
SELECT p.slug, p.kind, p.published_at, p.listed, p.sort_order,
       p.updated_at, p.updated_by,
       COALESCE(array_agg(l.locale ORDER BY l.locale)
                FILTER (WHERE l.locale IS NOT NULL), '{}')::text[] AS locales
FROM info_pages p
LEFT JOIN info_page_localizations l ON l.page_slug = p.slug
GROUP BY p.slug
ORDER BY p.sort_order, p.slug;

-- name: GetInfoPage :one
SELECT slug, kind, published_at, listed, sort_order, updated_at, updated_by
FROM info_pages WHERE slug = $1;

-- name: GetInfoPageLocalizations :many
SELECT locale, title, body FROM info_page_localizations
WHERE page_slug = $1 ORDER BY locale;

-- name: UpsertInfoPage :one
INSERT INTO info_pages (slug, kind, listed, sort_order, updated_by)
VALUES ($1, $2, $3, $4, sqlc.narg(updated_by))
ON CONFLICT (slug) DO UPDATE
SET kind = EXCLUDED.kind,
    listed = EXCLUDED.listed,
    sort_order = EXCLUDED.sort_order,
    updated_at = now(),
    updated_by = EXCLUDED.updated_by
RETURNING slug, kind, published_at, listed, sort_order, updated_at, updated_by;

-- name: UpsertInfoPageLocalization :exec
INSERT INTO info_page_localizations (page_slug, locale, title, body)
VALUES ($1, $2, $3, $4)
ON CONFLICT (page_slug, locale) DO UPDATE
SET title = EXCLUDED.title, body = EXCLUDED.body;

-- name: DeleteInfoPageLocalization :exec
DELETE FROM info_page_localizations WHERE page_slug = $1 AND locale = $2;

-- name: SetInfoPagePublication :one
-- Publishing and unpublishing are the same statement, because the difference is
-- one nullable column and splitting them would let the two drift.
UPDATE info_pages
SET published_at = CASE WHEN sqlc.arg(published)::boolean
                        THEN COALESCE(published_at, now()) ELSE NULL END,
    updated_at = now(),
    updated_by = sqlc.narg(updated_by)
WHERE slug = sqlc.arg(slug)
RETURNING slug, kind, published_at, listed, sort_order, updated_at, updated_by;

-- name: DeleteInfoPage :execrows
DELETE FROM info_pages WHERE slug = $1;
