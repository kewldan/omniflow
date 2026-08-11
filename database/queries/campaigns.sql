-- News authoring, segments, templates, and campaigns.

-- ---------------------------------------------------------------------------
-- News
-- ---------------------------------------------------------------------------

-- name: SearchNewsPosts :many
SELECT
  sqlc.embed(p),
  COALESCE(en.title, '') AS title_en,
  COALESCE(ru.title, '') AS title_ru
FROM news_posts p
LEFT JOIN news_post_localizations en ON en.post_id = p.id AND en.locale = 'en'
LEFT JOIN news_post_localizations ru ON ru.post_id = p.id AND ru.locale = 'ru'
WHERE (sqlc.narg(status)::text IS NULL OR p.status = sqlc.narg(status)::text)
ORDER BY p.created_at DESC
LIMIT sqlc.arg(page_size);

-- name: UpsertNewsPost :one
INSERT INTO news_posts (slug, category, class, created_by)
VALUES (sqlc.arg(slug), sqlc.arg(category), sqlc.arg(class), sqlc.narg(created_by))
ON CONFLICT (slug) DO UPDATE
  SET category = EXCLUDED.category, class = EXCLUDED.class, updated_at = now()
RETURNING *;

-- name: SetNewsPostState :one
-- Publication is recorded, not inferred. `published_at` is set once and kept, so
-- unpublishing and republishing does not rewrite when customers first saw it.
UPDATE news_posts
SET status = sqlc.arg(status),
    scheduled_for = sqlc.narg(scheduled_for),
    published_at = CASE
      WHEN sqlc.arg(status)::text = 'published' THEN COALESCE(published_at, now())
      ELSE published_at END,
    unpublished_at = CASE
      WHEN sqlc.arg(status)::text = 'unpublished' THEN now() ELSE NULL END,
    archived_at = CASE
      WHEN sqlc.arg(status)::text = 'archived' THEN now() ELSE NULL END,
    updated_at = now()
WHERE id = sqlc.arg(post_id)
RETURNING *;

-- name: ListDueNewsPosts :many
SELECT * FROM news_posts
WHERE status = 'scheduled' AND scheduled_for <= now()
ORDER BY scheduled_for
LIMIT sqlc.arg(page_size);

-- ---------------------------------------------------------------------------
-- Segments and templates
-- ---------------------------------------------------------------------------

-- name: ListAudienceSegments :many
SELECT * FROM audience_segments WHERE archived_at IS NULL ORDER BY code;

-- name: GetAudienceSegment :one
SELECT * FROM audience_segments WHERE id = $1;

-- name: UpsertAudienceSegment :one
INSERT INTO audience_segments (code, name_en, name_ru, filters, created_by)
VALUES (sqlc.arg(code), sqlc.arg(name_en), sqlc.arg(name_ru), sqlc.arg(filters),
        sqlc.narg(created_by))
ON CONFLICT (code) DO UPDATE SET
  name_en = EXCLUDED.name_en, name_ru = EXCLUDED.name_ru,
  filters = EXCLUDED.filters, archived_at = NULL, updated_at = now()
RETURNING *;

-- name: ListMessageTemplates :many
SELECT * FROM message_templates WHERE archived_at IS NULL ORDER BY code;

-- name: GetMessageTemplate :one
SELECT * FROM message_templates WHERE id = $1;

-- name: UpsertMessageTemplate :one
INSERT INTO message_templates (
  code, class, subject_en, subject_ru, body_en, body_ru, variables, updated_by
) VALUES (
  sqlc.arg(code), sqlc.arg(class), sqlc.arg(subject_en), sqlc.arg(subject_ru),
  sqlc.arg(body_en), sqlc.arg(body_ru), sqlc.arg(variables), sqlc.narg(updated_by)
)
ON CONFLICT (code) DO UPDATE SET
  class = EXCLUDED.class, subject_en = EXCLUDED.subject_en, subject_ru = EXCLUDED.subject_ru,
  body_en = EXCLUDED.body_en, body_ru = EXCLUDED.body_ru,
  variables = EXCLUDED.variables, archived_at = NULL,
  updated_at = now(), updated_by = EXCLUDED.updated_by
RETURNING *;

-- ---------------------------------------------------------------------------
-- Campaigns
-- ---------------------------------------------------------------------------

-- name: ListCampaigns :many
SELECT sqlc.embed(c), t.code AS template_code, s.code AS segment_code
FROM campaigns c
JOIN message_templates t ON t.id = c.template_id
JOIN audience_segments s ON s.id = c.segment_id
ORDER BY c.created_at DESC
LIMIT sqlc.arg(page_size);

-- name: GetCampaign :one
SELECT sqlc.embed(c), t.code AS template_code, s.code AS segment_code, s.filters
FROM campaigns c
JOIN message_templates t ON t.id = c.template_id
JOIN audience_segments s ON s.id = c.segment_id
WHERE c.id = $1;

-- name: CreateCampaign :one
INSERT INTO campaigns (name, template_id, segment_id, estimated_audience, created_by)
VALUES (sqlc.arg(name), sqlc.arg(template_id), sqlc.arg(segment_id),
        sqlc.arg(estimated_audience), sqlc.narg(created_by))
RETURNING *;

-- name: SetCampaignState :one
-- The permitted transitions are enforced here rather than in the caller: a
-- campaign that could jump from draft to completed would report a reach it never
-- had.
UPDATE campaigns
SET status = sqlc.arg(status),
    scheduled_for = sqlc.narg(scheduled_for),
    started_at = CASE WHEN sqlc.arg(status)::text = 'running'
      THEN COALESCE(started_at, now()) ELSE started_at END,
    completed_at = CASE WHEN sqlc.arg(status)::text IN ('completed', 'cancelled')
      THEN now() ELSE NULL END,
    updated_at = now()
WHERE id = sqlc.arg(campaign_id)
  AND status = ANY(sqlc.arg(allowed_from)::text[])
RETURNING *;

-- name: ListDueCampaigns :many
SELECT * FROM campaigns
WHERE (status = 'scheduled' AND scheduled_for <= now()) OR status = 'running'
ORDER BY scheduled_for NULLS FIRST
LIMIT sqlc.arg(page_size);

-- name: QueueCampaignRecipient :exec
-- The primary key is the deduplication: a paused-and-resumed campaign continues
-- rather than restarting, and a recipient cannot be queued twice.
INSERT INTO campaign_recipients (campaign_id, user_id)
VALUES (sqlc.arg(campaign_id), sqlc.arg(user_id))
ON CONFLICT DO NOTHING;

-- name: ListPendingCampaignRecipients :many
SELECT r.user_id, u.locale, rm.telegram_id
FROM campaign_recipients r
JOIN users u ON u.id = r.user_id
LEFT JOIN remnawave_users rm ON rm.user_id = r.user_id
WHERE r.campaign_id = sqlc.arg(campaign_id) AND r.status = 'queued'
ORDER BY r.queued_at
LIMIT sqlc.arg(page_size);

-- name: ResolveCampaignRecipient :exec
UPDATE campaign_recipients
SET status = sqlc.arg(status),
    suppression_reason = sqlc.narg(suppression_reason),
    error_code = sqlc.narg(error_code),
    resolved_at = now()
WHERE campaign_id = sqlc.arg(campaign_id) AND user_id = sqlc.arg(user_id)
  AND status = 'queued';

-- name: RecountCampaign :one
UPDATE campaigns c
SET queued_count = counts.queued,
    sent_count = counts.sent,
    failed_count = counts.failed,
    suppressed_count = counts.suppressed,
    updated_at = now()
FROM (
  SELECT
    count(*) FILTER (WHERE status = 'queued')::integer AS queued,
    count(*) FILTER (WHERE status = 'sent')::integer AS sent,
    count(*) FILTER (WHERE status IN ('failed', 'blocked'))::integer AS failed,
    count(*) FILTER (WHERE status = 'suppressed')::integer AS suppressed
  FROM campaign_recipients WHERE campaign_id = sqlc.arg(campaign_id)
) counts
WHERE c.id = sqlc.arg(campaign_id)
RETURNING c.*;

-- ---------------------------------------------------------------------------
-- Suppression
-- ---------------------------------------------------------------------------

-- name: ListSuppressions :many
SELECT s.*, COALESCE(a.display_name, '') AS created_by_name
FROM communication_suppressions s
LEFT JOIN admin_users a ON a.id = s.created_by
ORDER BY s.created_at DESC
LIMIT sqlc.arg(page_size);

-- name: SuppressCustomer :one
INSERT INTO communication_suppressions (user_id, reason, note, created_by)
VALUES (sqlc.arg(user_id), sqlc.arg(reason), sqlc.narg(note), sqlc.narg(created_by))
ON CONFLICT (user_id) DO UPDATE SET
  reason = EXCLUDED.reason, note = EXCLUDED.note, created_by = EXCLUDED.created_by
RETURNING *;

-- name: UnsuppressCustomer :exec
DELETE FROM communication_suppressions WHERE user_id = $1;

-- name: IsSuppressed :one
SELECT EXISTS (SELECT 1 FROM communication_suppressions WHERE user_id = $1) AS suppressed;
