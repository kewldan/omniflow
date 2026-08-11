-- Gifts and personal offers.
--
-- A gift is bought by one customer for another, so every query here is careful
-- about which side it is answering for: the sender sees their own order and a
-- code hint, the recipient sees only what they were given.

-- ---------------------------------------------------------------------------
-- Gifts
-- ---------------------------------------------------------------------------

-- name: CreateGift :one
INSERT INTO gifts (
  order_id, sender_user_id, kind, plan_version_id, addon_version_id, credit_minor,
  currency, code_hash, code_hint, recipient_telegram_id, sender_message, expires_at
) VALUES (
  sqlc.arg(order_id), sqlc.arg(sender_user_id), sqlc.arg(kind),
  sqlc.narg(plan_version_id), sqlc.narg(addon_version_id), sqlc.narg(credit_minor),
  sqlc.arg(currency), sqlc.arg(code_hash), sqlc.arg(code_hint),
  sqlc.narg(recipient_telegram_id), sqlc.narg(sender_message),
  now() + sqlc.arg(lifetime)::interval
)
ON CONFLICT (order_id) DO NOTHING
RETURNING *;

-- name: GetGift :one
SELECT * FROM gifts WHERE id = $1;

-- name: GetGiftByOrder :one
SELECT * FROM gifts WHERE order_id = $1;

-- name: GetGiftByCodeHash :one
-- The claim path's only lookup. The plaintext code never reaches the database.
SELECT * FROM gifts WHERE code_hash = $1;

-- name: LockGift :one
-- Serialises claim, revoke, and expiry against one another. Without it two
-- simultaneous claims of one code could both observe 'deliverable'.
SELECT * FROM gifts WHERE id = $1 FOR UPDATE;

-- name: MarkGiftDeliverable :one
-- Settlement of the sender's order is what makes a gift claimable. Restricting
-- the update to 'pending' means a replayed settlement changes nothing.
UPDATE gifts
SET status = 'deliverable', updated_at = now()
WHERE id = sqlc.arg(gift_id) AND status = 'pending'
RETURNING *;

-- name: ClaimGift :one
-- Single redemption by construction: the predicate only matches a deliverable,
-- unexpired gift, so a second claim of the same code updates no row.
UPDATE gifts
SET status = 'claimed',
    recipient_user_id = sqlc.arg(recipient_user_id),
    claim_entitlement_id = sqlc.narg(claim_entitlement_id),
    claim_ledger_transaction_id = sqlc.narg(claim_ledger_transaction_id),
    claimed_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(gift_id)
  AND status = 'deliverable'
  AND expires_at > now()
RETURNING *;

-- name: RecordGiftClaimAttempt :one
-- Counts a failed redemption so a code cannot be brute-forced indefinitely. The
-- rate limiter in front of the endpoint is the first defence; this is the
-- durable one that survives a restart.
UPDATE gifts
SET claim_attempts = claim_attempts + 1, updated_at = now()
WHERE id = sqlc.arg(gift_id)
RETURNING *;

-- name: RevokeGift :one
-- An operator may reclaim a gift that has not been redeemed. A claimed gift is
-- deliberately not revocable: the recipient already holds what it bought, and
-- taking it back is a refund decision with its own record.
UPDATE gifts
SET status = 'revoked',
    revoked_at = now(),
    revoked_by = sqlc.arg(revoked_by),
    revoke_reason = sqlc.arg(revoke_reason),
    updated_at = now()
WHERE id = sqlc.arg(gift_id) AND status IN ('pending', 'deliverable', 'expired')
RETURNING *;

-- name: MarkGiftRefunded :one
UPDATE gifts
SET status = 'refunded', updated_at = now()
WHERE id = sqlc.arg(gift_id) AND status IN ('revoked', 'expired')
RETURNING *;

-- name: ExpireGifts :many
UPDATE gifts
SET status = 'expired', updated_at = now()
WHERE status = 'deliverable' AND expires_at <= now()
RETURNING *;

-- name: ListGiftsForSender :many
SELECT * FROM gifts
WHERE sender_user_id = $1
ORDER BY created_at DESC
LIMIT sqlc.arg(page_size);

-- name: ListGiftsReceived :many
SELECT * FROM gifts
WHERE recipient_user_id = $1 AND status = 'claimed'
ORDER BY claimed_at DESC
LIMIT sqlc.arg(page_size);

-- name: SearchGifts :many
-- The panel's gift register. Keyset pagination on (created_at, id).
SELECT * FROM gifts
WHERE (
    sqlc.narg(cursor_created_at)::timestamptz IS NULL
    OR (created_at, id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
  AND (sqlc.narg(kind)::text IS NULL OR kind = sqlc.narg(kind))
  AND (sqlc.narg(sender_user_id)::uuid IS NULL OR sender_user_id = sqlc.narg(sender_user_id))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_size);

-- name: CountGiftsByStatus :many
SELECT status, count(*)::bigint AS total FROM gifts GROUP BY status ORDER BY status;

-- ---------------------------------------------------------------------------
-- Personal offers
-- ---------------------------------------------------------------------------

-- name: CreatePersonalOffer :one
-- The partial unique index on active offers is what makes a re-run of a
-- targeting job idempotent: the second insert conflicts and returns nothing.
INSERT INTO personal_offers (
  user_id, promotion_id, plan_id, title_ru, title_en, terms_ru, terms_en,
  starts_at, expires_at, created_by
) VALUES (
  sqlc.arg(user_id), sqlc.arg(promotion_id), sqlc.narg(plan_id),
  sqlc.arg(title_ru), sqlc.arg(title_en), sqlc.arg(terms_ru), sqlc.arg(terms_en),
  sqlc.arg(starts_at), sqlc.arg(expires_at), sqlc.narg(created_by)
)
ON CONFLICT (user_id, promotion_id) WHERE status = 'active' DO NOTHING
RETURNING *;

-- name: GetPersonalOffer :one
SELECT * FROM personal_offers WHERE id = $1;

-- name: ListActiveOffersForCustomer :many
-- What the bot shows. Expired-but-not-yet-swept rows are filtered here rather
-- than relying on the sweeper having run, so an offer never outlives its window
-- on screen.
SELECT sqlc.embed(o), sqlc.embed(p)
FROM personal_offers o
JOIN promotions p ON p.id = o.promotion_id
WHERE o.user_id = $1
  AND o.status = 'active'
  AND o.starts_at <= now()
  AND o.expires_at > now()
ORDER BY o.expires_at
LIMIT sqlc.arg(page_size);

-- name: GetActiveOfferForPromotion :one
SELECT * FROM personal_offers
WHERE user_id = sqlc.arg(user_id)
  AND promotion_id = sqlc.arg(promotion_id)
  AND status = 'active'
  AND starts_at <= now()
  AND expires_at > now();

-- name: RedeemPersonalOffer :one
UPDATE personal_offers
SET status = 'redeemed', order_id = sqlc.arg(order_id), resolved_at = now()
WHERE id = sqlc.arg(offer_id) AND status = 'active' AND expires_at > now()
RETURNING *;

-- name: DismissPersonalOffer :one
UPDATE personal_offers
SET status = 'dismissed', resolved_at = now()
WHERE id = sqlc.arg(offer_id) AND user_id = sqlc.arg(user_id) AND status = 'active'
RETURNING *;

-- name: RevokePersonalOffer :one
UPDATE personal_offers
SET status = 'revoked', resolved_at = now()
WHERE id = sqlc.arg(offer_id) AND status = 'active'
RETURNING *;

-- name: ExpirePersonalOffers :execrows
UPDATE personal_offers
SET status = 'expired', resolved_at = now()
WHERE status = 'active' AND expires_at <= now();

-- name: SearchPersonalOffers :many
SELECT * FROM personal_offers
WHERE (
    sqlc.narg(cursor_created_at)::timestamptz IS NULL
    OR (created_at, id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
  AND (sqlc.narg(user_id)::uuid IS NULL OR user_id = sqlc.narg(user_id))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_size);
