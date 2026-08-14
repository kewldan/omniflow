-- Wholesale code batches.

-- name: CreateCodeBatch :one
INSERT INTO code_batches (
  reference, plan_version_id, quantity, unit_price_minor, currency,
  note, expires_at, created_by
)
VALUES ($1, $2, $3, $4, $5, sqlc.narg(note), sqlc.narg(expires_at), sqlc.narg(created_by))
RETURNING *;

-- name: InsertAccessCode :exec
INSERT INTO access_codes (batch_id, code_hash, code_hint)
VALUES ($1, $2, $3);

-- name: ListCodeBatches :many
-- One row per batch with its counts, so the list answers "how many are still
-- out there" without a second query per row.
SELECT b.id, b.reference, b.quantity, b.unit_price_minor, b.currency, b.note,
       b.expires_at, b.revoked_at, b.revoke_reason, b.created_at, b.created_by,
       p.code AS plan_code, v.version AS plan_version, v.billing_period,
       count(*) FILTER (WHERE c.status = 'issued')::bigint AS issued,
       count(*) FILTER (WHERE c.status = 'redeemed')::bigint AS redeemed,
       count(*) FILTER (WHERE c.status = 'revoked')::bigint AS revoked
FROM code_batches b
JOIN plan_versions v ON v.id = b.plan_version_id
JOIN plans p ON p.id = v.plan_id
LEFT JOIN access_codes c ON c.batch_id = b.id
GROUP BY b.id, p.code, v.version, v.billing_period
ORDER BY b.created_at DESC
LIMIT sqlc.arg(page_size);

-- name: ListBatchCodes :many
-- The hints and statuses of one batch's codes. Never the codes themselves:
-- there is nothing in this table that could produce one.
SELECT id, code_hint, status, redeemed_by, redeemed_at
FROM access_codes
WHERE batch_id = $1
ORDER BY created_at, code_hint;

-- name: RevokeBatchCodes :execrows
-- Kills every code in the batch that nobody has redeemed.
--
-- A redeemed code is untouched by design: somebody is using the subscription it
-- produced, and taking that back is a different decision from withdrawing an
-- unused list. The affected count comes back so the panel reports how many were
-- actually killed rather than how many the operator imagined.
UPDATE access_codes SET status = 'revoked'
WHERE batch_id = sqlc.arg(batch_id) AND status = 'issued';

-- name: MarkCodeBatchRevoked :one
-- Records who withdrew the batch and why.
--
-- Both COALESCE calls make a second revocation a no-op rather than a rewrite of
-- the first one's reason, which is the record somebody reads six months later
-- when they ask why three hundred codes were killed.
UPDATE code_batches
SET revoked_at = COALESCE(revoked_at, now()),
    revoked_by = COALESCE(revoked_by, sqlc.narg(revoked_by)),
    revoke_reason = COALESCE(revoke_reason, sqlc.arg(reason))
WHERE id = sqlc.arg(batch_id)
RETURNING *;

-- name: RedeemAccessCode :one
-- Single redemption as a property of the predicate rather than of timing.
--
-- Only an `issued` code in a batch that is neither revoked nor expired matches,
-- and the same statement is what writes the redemption — so two simultaneous
-- attempts on one code produce one entitlement and one refusal, without a lock
-- anybody has to remember to take.
UPDATE access_codes
SET status = 'redeemed', redeemed_by = sqlc.arg(redeemed_by), redeemed_at = now()
WHERE code_hash = sqlc.arg(code_hash)
  AND status = 'issued'
  AND EXISTS (
    SELECT 1 FROM code_batches b
    WHERE b.id = access_codes.batch_id
      AND b.revoked_at IS NULL
      AND (b.expires_at IS NULL OR b.expires_at > now())
  )
RETURNING id, batch_id, code_hint;

-- name: GetCodeBatchForRedemption :one
SELECT b.id, b.plan_version_id, b.currency, v.duration_seconds,
       v.traffic_allowance_bytes, v.device_limit, v.remnawave_squad_ids,
       v.grace_period_seconds
FROM code_batches b
JOIN plan_versions v ON v.id = b.plan_version_id
WHERE b.id = $1;

-- name: AttachAccessCodeEntitlement :exec
UPDATE access_codes SET redeemed_entitlement_id = $2 WHERE id = $1;
