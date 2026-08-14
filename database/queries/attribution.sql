-- Advertising attribution and the offline-conversion export.

-- The write lives in `internal/accountcheckout` beside the rest of the purchase
-- path rather than here: that package owns its own SQL, and the ownership check
-- the write is gated on is a checkout concern rather than a reporting one.

-- name: ConversionsInPeriod :many
-- The offline-conversion export: settled orders that came from an advertisement.
--
-- Keyed on `paid_at`, not `created_at`. An advertising platform is being told
-- when the money arrived so it can attribute the click it sold; the date an
-- abandoned draft was created answers a different question.
--
-- Refunded orders are included with their refunded amount reported separately
-- rather than being dropped. A platform that optimised bidding on a sale that
-- was later returned is being taught the wrong thing, and an operator can only
-- correct for that if the export says it happened.
SELECT o.id AS order_id, o.operation, o.currency, o.paid_minor, o.refunded_minor,
       o.state, o.paid_at,
       COALESCE(a.click_id, '') AS click_id,
       COALESCE(a.click_source, '') AS click_source,
       COALESCE(a.utm_source, '') AS utm_source,
       COALESCE(a.utm_medium, '') AS utm_medium,
       COALESCE(a.utm_campaign, '') AS utm_campaign,
       COALESCE(a.utm_content, '') AS utm_content,
       COALESCE(a.utm_term, '') AS utm_term
FROM orders o
JOIN order_attributions a ON a.order_id = o.id
WHERE o.paid_at >= sqlc.arg(from_time) AND o.paid_at < sqlc.arg(to_time)
  AND (sqlc.narg(click_source)::text IS NULL OR a.click_source = sqlc.narg(click_source)::text)
ORDER BY o.paid_at, o.id
LIMIT sqlc.arg(page_size);

-- name: AttributionSummary :many
-- What each channel brought in, which is the question an operator asks before
-- they export anything.
SELECT COALESCE(NULLIF(a.utm_source, ''), a.click_source, 'direct') AS channel,
       COALESCE(a.utm_medium, '') AS medium,
       count(*) AS orders,
       count(*) FILTER (WHERE a.click_id IS NOT NULL) AS attributed_clicks,
       o.currency,
       sum(o.paid_minor)::bigint AS paid_minor,
       sum(o.refunded_minor)::bigint AS refunded_minor
FROM orders o
JOIN order_attributions a ON a.order_id = o.id
WHERE o.paid_at >= sqlc.arg(from_time) AND o.paid_at < sqlc.arg(to_time)
GROUP BY channel, medium, o.currency
ORDER BY paid_minor DESC, channel;
