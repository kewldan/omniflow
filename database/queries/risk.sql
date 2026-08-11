-- External blocklists and anomaly detection.
--
-- Both surfaces produce evidence for an operator, never an automatic sanction.
-- No query in this file suspends a customer, cancels an order, or moves money;
-- the panel's ordinary customer and finance mutations do that, with their own
-- permission checks and audit events, once a human has decided.

-- ---------------------------------------------------------------------------
-- Blocklist sources
-- ---------------------------------------------------------------------------

-- name: ListBlocklistSources :many
SELECT * FROM blocklist_sources ORDER BY display_name;

-- name: GetBlocklistSource :one
SELECT * FROM blocklist_sources WHERE id = $1;

-- name: GetBlocklistSourceBySlug :one
SELECT * FROM blocklist_sources WHERE slug = $1;

-- name: UpsertBlocklistSource :one
-- A null auth header on update means "keep the stored credential", matching the
-- write-only treatment every other secret in the schema gets.
INSERT INTO blocklist_sources (
  slug, display_name, subject_kind, url, auth_header_ciphertext,
  enabled, refresh_interval_seconds, updated_by
) VALUES (
  sqlc.arg(slug), sqlc.arg(display_name), sqlc.arg(subject_kind), sqlc.arg(url),
  sqlc.narg(auth_header_ciphertext), sqlc.arg(enabled),
  sqlc.arg(refresh_interval_seconds), sqlc.narg(updated_by)
)
ON CONFLICT (slug) DO UPDATE
SET display_name = EXCLUDED.display_name,
    subject_kind = EXCLUDED.subject_kind,
    url = EXCLUDED.url,
    auth_header_ciphertext = COALESCE(
      EXCLUDED.auth_header_ciphertext, blocklist_sources.auth_header_ciphertext
    ),
    enabled = EXCLUDED.enabled,
    refresh_interval_seconds = EXCLUDED.refresh_interval_seconds,
    updated_by = EXCLUDED.updated_by,
    updated_at = now()
RETURNING *;

-- name: DeleteBlocklistSource :exec
DELETE FROM blocklist_sources WHERE id = $1;

-- name: ListDueBlocklistSources :many
SELECT * FROM blocklist_sources
WHERE enabled AND next_refresh_at <= now()
ORDER BY next_refresh_at
LIMIT sqlc.arg(page_size);

-- name: RecordBlocklistRefresh :one
UPDATE blocklist_sources
SET status = sqlc.arg(status),
    last_error_code = sqlc.narg(last_error_code),
    entry_count = sqlc.arg(entry_count),
    last_refresh_at = now(),
    next_refresh_at = now() + make_interval(secs => refresh_interval_seconds),
    updated_at = now()
WHERE id = sqlc.arg(source_id)
RETURNING *;

-- ---------------------------------------------------------------------------
-- Blocklist entries
-- ---------------------------------------------------------------------------

-- name: DeleteBlocklistEntries :exec
-- A refresh replaces a source's entries wholesale inside one transaction, so an
-- entry the publisher removed stops matching in the same instant the new set
-- starts.
DELETE FROM blocklist_entries WHERE source_id = $1;

-- name: InsertBlocklistEntry :exec
INSERT INTO blocklist_entries (source_id, value_fingerprint, reason_code)
VALUES (sqlc.arg(source_id), sqlc.arg(value_fingerprint), sqlc.narg(reason_code))
ON CONFLICT (source_id, value_fingerprint) DO NOTHING;

-- name: MatchBlocklistFingerprints :many
-- Given the fingerprints of a customer's identifiers, returns every enabled
-- source that lists one of them. An exact index probe per source, so the check
-- is cheap enough to run on the sign-up and purchase paths.
SELECT sqlc.embed(e), s.slug, s.display_name, s.subject_kind
FROM blocklist_entries e
JOIN blocklist_sources s ON s.id = e.source_id AND s.enabled
WHERE e.value_fingerprint = ANY(sqlc.arg(fingerprints)::bytea[]);

-- ---------------------------------------------------------------------------
-- Matches and adjudication
-- ---------------------------------------------------------------------------

-- name: UpsertBlocklistMatch :one
-- Re-detecting a match an operator has already decided must not reopen it: the
-- conflict clause deliberately touches nothing, so an allow decision survives
-- every subsequent refresh of the source.
INSERT INTO blocklist_matches (user_id, source_id, subject_kind, value_fingerprint)
VALUES (sqlc.arg(user_id), sqlc.arg(source_id), sqlc.arg(subject_kind), sqlc.arg(value_fingerprint))
ON CONFLICT (user_id, source_id, value_fingerprint) DO NOTHING
RETURNING *;

-- name: GetBlocklistMatch :one
SELECT * FROM blocklist_matches WHERE id = $1;

-- name: SearchBlocklistMatches :many
SELECT sqlc.embed(m), s.slug AS source_slug, s.display_name AS source_name
FROM blocklist_matches m
JOIN blocklist_sources s ON s.id = m.source_id
WHERE (
    sqlc.narg(cursor_detected_at)::timestamptz IS NULL
    OR (m.detected_at, m.id) < (sqlc.narg(cursor_detected_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
  AND (sqlc.narg(status)::text IS NULL OR m.status = sqlc.narg(status))
  AND (sqlc.narg(user_id)::uuid IS NULL OR m.user_id = sqlc.narg(user_id))
ORDER BY m.detected_at DESC, m.id DESC
LIMIT sqlc.arg(page_size);

-- name: ListBlocklistMatchesForCustomer :many
SELECT sqlc.embed(m), s.slug AS source_slug, s.display_name AS source_name
FROM blocklist_matches m
JOIN blocklist_sources s ON s.id = m.source_id
WHERE m.user_id = $1
ORDER BY m.detected_at DESC;

-- name: DecideBlocklistMatch :one
-- The only path that moves a match out of review. The deciding operator and
-- their reason are required by the row itself, so an adverse decision cannot be
-- recorded anonymously.
UPDATE blocklist_matches
SET status = sqlc.arg(status),
    decision_reason = sqlc.arg(decision_reason),
    decided_by = sqlc.arg(decided_by),
    decided_at = now()
WHERE id = sqlc.arg(match_id) AND status IN ('open', 'appealed')
RETURNING *;

-- name: AppealBlocklistMatch :one
UPDATE blocklist_matches
SET status = 'appealed', decision_reason = sqlc.arg(decision_reason),
    decided_by = NULL, decided_at = NULL
WHERE id = sqlc.arg(match_id) AND status = 'blocked'
RETURNING *;

-- name: CountOpenBlocklistMatches :one
SELECT count(*)::bigint FROM blocklist_matches WHERE status IN ('open', 'appealed');

-- name: AddBlocklistAllowlistEntry :one
INSERT INTO blocklist_allowlist (user_id, reason, added_by)
VALUES (sqlc.arg(user_id), sqlc.arg(reason), sqlc.narg(added_by))
ON CONFLICT (user_id) DO UPDATE
SET reason = EXCLUDED.reason, added_by = EXCLUDED.added_by, created_at = now()
RETURNING *;

-- name: RemoveBlocklistAllowlistEntry :exec
DELETE FROM blocklist_allowlist WHERE user_id = $1;

-- name: IsBlocklistAllowlisted :one
SELECT EXISTS (SELECT 1 FROM blocklist_allowlist WHERE user_id = $1)::boolean;

-- ---------------------------------------------------------------------------
-- Anomaly rules and signals
-- ---------------------------------------------------------------------------

-- name: ListAnomalyRules :many
SELECT * FROM anomaly_rules ORDER BY metric;

-- name: GetAnomalyRule :one
SELECT * FROM anomaly_rules WHERE metric = $1;

-- name: UpsertAnomalyRule :one
INSERT INTO anomaly_rules (
  metric, enabled, window_seconds, warn_threshold, alert_threshold, minimum_sample, updated_by
) VALUES (
  sqlc.arg(metric), sqlc.arg(enabled), sqlc.arg(window_seconds),
  sqlc.arg(warn_threshold), sqlc.arg(alert_threshold), sqlc.arg(minimum_sample),
  sqlc.narg(updated_by)
)
ON CONFLICT (metric) DO UPDATE
SET enabled = EXCLUDED.enabled,
    window_seconds = EXCLUDED.window_seconds,
    warn_threshold = EXCLUDED.warn_threshold,
    alert_threshold = EXCLUDED.alert_threshold,
    minimum_sample = EXCLUDED.minimum_sample,
    updated_by = EXCLUDED.updated_by,
    updated_at = now()
RETURNING *;

-- name: RaiseAnomalySignal :one
-- A condition that persists across several evaluation runs is one signal, not
-- one per run: the dedupe key collides and the existing row is refreshed with
-- the newest observation. A signal an operator already reviewed stays reviewed,
-- because the conflict clause never resets `status`.
INSERT INTO anomaly_signals (
  metric, severity, subject_type, subject_id, observed, threshold, sample_size,
  window_started_at, window_ended_at, evidence, dedupe_key
) VALUES (
  sqlc.arg(metric), sqlc.arg(severity), sqlc.arg(subject_type), sqlc.arg(subject_id),
  sqlc.arg(observed), sqlc.arg(threshold), sqlc.arg(sample_size),
  sqlc.arg(window_started_at), sqlc.arg(window_ended_at), sqlc.arg(evidence),
  sqlc.arg(dedupe_key)
)
ON CONFLICT (metric, dedupe_key) DO UPDATE
SET observed = EXCLUDED.observed,
    severity = EXCLUDED.severity,
    sample_size = EXCLUDED.sample_size,
    window_ended_at = EXCLUDED.window_ended_at,
    evidence = EXCLUDED.evidence
RETURNING *;

-- name: GetAnomalySignal :one
SELECT * FROM anomaly_signals WHERE id = $1;

-- name: SearchAnomalySignals :many
SELECT * FROM anomaly_signals
WHERE (
    sqlc.narg(cursor_detected_at)::timestamptz IS NULL
    OR (detected_at, id) < (sqlc.narg(cursor_detected_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
  AND (sqlc.narg(metric)::text IS NULL OR metric = sqlc.narg(metric))
  AND (sqlc.narg(severity)::text IS NULL OR severity = sqlc.narg(severity))
ORDER BY detected_at DESC, id DESC
LIMIT sqlc.arg(page_size);

-- name: ReviewAnomalySignal :one
UPDATE anomaly_signals
SET status = sqlc.arg(status),
    reviewed_by = sqlc.arg(reviewed_by),
    review_reason = sqlc.narg(review_reason),
    reviewed_at = now()
WHERE id = sqlc.arg(signal_id) AND status = 'open'
RETURNING *;

-- name: CountOpenAnomalySignals :one
SELECT count(*)::bigint FROM anomaly_signals WHERE status = 'open';

-- ---------------------------------------------------------------------------
-- Observations the rules are evaluated against
-- ---------------------------------------------------------------------------
--
-- Each returns a raw aggregate. The comparison against a threshold, the
-- severity, and the dedupe key are decided in `internal/anomaly`, which is
-- where they can be unit-tested without a database.

-- name: ObservePurchaseVolume :many
-- Paid order value per customer inside the window. Per-customer rather than
-- installation-wide because a spike concentrated on one account is the shape
-- that matters; the installation total is on the dashboard already.
SELECT o.user_id, o.currency,
       count(*)::bigint AS order_count,
       COALESCE(sum(o.paid_minor), 0)::bigint AS paid_minor
FROM orders o
WHERE o.state IN ('paid', 'fulfilled', 'partially_refunded')
  AND o.updated_at >= sqlc.arg(window_start)
  AND o.updated_at < sqlc.arg(window_end)
GROUP BY o.user_id, o.currency
HAVING COALESCE(sum(o.paid_minor), 0) >= sqlc.arg(minimum_minor);

-- name: ObserveRefundVolume :many
SELECT o.user_id, r.currency,
       count(*)::bigint AS refund_count,
       COALESCE(sum(r.amount_minor), 0)::bigint AS refunded_minor
FROM refunds r
JOIN payment_intents i ON i.id = r.payment_intent_id
JOIN orders o ON o.id = i.order_id
WHERE r.status = 'succeeded'
  AND r.updated_at >= sqlc.arg(window_start)
  AND r.updated_at < sqlc.arg(window_end)
GROUP BY o.user_id, r.currency
HAVING COALESCE(sum(r.amount_minor), 0) >= sqlc.arg(minimum_minor);

-- name: ObserveReferralVolume :many
-- Qualified referrals per inviter. A referral programme being farmed shows up
-- here long before it shows up in the ledger.
SELECT a.referrer_user_id AS user_id, count(*)::bigint AS qualified_count
FROM referral_attributions a
WHERE a.qualified_at >= sqlc.arg(window_start)
  AND a.qualified_at < sqlc.arg(window_end)
GROUP BY a.referrer_user_id
HAVING count(*) >= sqlc.arg(minimum_count);

-- name: ObserveTrafficUsage :many
-- Remnawave owns traffic, and Omniflow only holds the counter it last observed.
-- That makes this a level check rather than a rate: a subscription whose
-- observed usage has passed the threshold, not one whose usage grew quickly.
-- The distinction is why the rule's window only controls how often a signal is
-- re-raised, and it is documented on the operator page as well.
SELECT s.id AS subscription_id, s.user_id,
       (COALESCE((s.observed_state->>'usedTrafficBytes')::bigint, 0))::bigint AS used_bytes,
       s.reconciled_at
FROM subscriptions s
WHERE s.status = 'active'
  AND s.observed_state ? 'usedTrafficBytes'
  AND COALESCE((s.observed_state->>'usedTrafficBytes')::bigint, 0) >= sqlc.arg(threshold_bytes)::bigint
ORDER BY used_bytes DESC
LIMIT sqlc.arg(page_size);
