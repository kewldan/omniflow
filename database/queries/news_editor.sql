-- The panel's news editor. Listing and publication state live in
-- campaigns.sql; these are the reads and writes the editor adds.

-- name: GetNewsPostForEdit :one
SELECT * FROM news_posts WHERE id = sqlc.arg(post_id);

-- name: ListNewsPostLocalizations :many
SELECT * FROM news_post_localizations WHERE post_id = sqlc.arg(post_id) ORDER BY locale;

-- name: UpdateNewsPostMeta :one
UPDATE news_posts
SET slug = sqlc.arg(slug), category_code = sqlc.arg(category_code), class = sqlc.arg(class),
    expires_at = sqlc.narg(expires_at), tags = sqlc.arg(tags)::text[], cover_url = sqlc.narg(cover_url),
    featured = sqlc.arg(featured), updated_at = now()
WHERE id = sqlc.arg(post_id)
RETURNING *;

-- name: UpsertNewsPostLocalization :exec
INSERT INTO news_post_localizations (post_id, locale, title, body)
VALUES (sqlc.arg(post_id), sqlc.arg(locale), sqlc.arg(title), sqlc.arg(body))
ON CONFLICT (post_id, locale) DO UPDATE SET title = EXCLUDED.title, body = EXCLUDED.body;

-- name: DeleteNewsPostLocalization :exec
DELETE FROM news_post_localizations WHERE post_id = sqlc.arg(post_id) AND locale = sqlc.arg(locale);

-- name: CountNewsReads :one
SELECT count(*)::bigint FROM news_reads WHERE post_id = sqlc.arg(post_id);
