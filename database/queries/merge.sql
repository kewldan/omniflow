-- Merging two customer accounts.
--
-- The preview and the move read the same counts from the same statements, so an
-- operator cannot be shown one number and have another happen.

-- name: MergeCandidate :one
-- What one side of a merge holds.
SELECT u.id, u.status, u.locale, u.timezone, u.created_at,
       u.merged_into, u.merged_at,
       (SELECT count(*) FROM subscriptions s
         WHERE s.user_id = u.id AND s.status = 'active')::bigint AS active_subscriptions,
       (SELECT count(*) FROM orders o WHERE o.user_id = u.id)::bigint AS orders,
       (SELECT count(*) FROM support_tickets t WHERE t.user_id = u.id)::bigint AS tickets,
       (SELECT count(*) FROM identities i
         WHERE i.user_id = u.id AND i.status = 'active')::bigint AS identities,
       (SELECT count(*) FROM referral_attributions r
         WHERE r.referrer_user_id = u.id)::bigint AS referrals_made,
       (SELECT count(*) FROM trial_claims c WHERE c.user_id = u.id)::bigint AS trial_claims,
       (SELECT count(*) FROM remnawave_users m WHERE m.user_id = u.id)::bigint AS remnawave_mappings
FROM users u
WHERE u.id = $1;

-- name: MergeWalletBalances :many
-- The source's balance per currency, which is what moves as a compensating pair
-- of ledger entries rather than as an UPDATE.
SELECT currency, COALESCE(sum(amount_minor), 0)::bigint AS balance_minor
FROM ledger_entries
-- The account type is stated even though the table's own constraint already
-- implies it: only a customer wallet entry carries a user. Naming it means this
-- query still reads correctly if another account type ever gains one.
WHERE account_type = 'customer_wallet' AND user_id = $1
GROUP BY currency
HAVING COALESCE(sum(amount_minor), 0) <> 0
ORDER BY currency;

-- name: MergeReferralBetween :one
-- Whether either account referred the other.
--
-- Merging them would make a customer their own referrer, which is a reward
-- somebody paid for a signup that turns out to be the same person. It is a
-- refusal rather than something to clean up silently.
SELECT count(*)::bigint AS between_them
FROM referral_attributions
WHERE (referrer_user_id = sqlc.arg(source) AND referred_user_id = sqlc.arg(target))
   OR (referrer_user_id = sqlc.arg(target) AND referred_user_id = sqlc.arg(source));

-- name: NextSubscriptionSlot :one
-- The first free slot on the target, so moved subscriptions do not collide with
-- the unique constraint on (user_id, slot).
SELECT COALESCE(max(slot), 0)::int + 1 AS next_slot
FROM subscriptions WHERE user_id = $1;

-- name: MoveSubscription :exec
UPDATE subscriptions SET user_id = sqlc.arg(target), slot = sqlc.arg(slot), updated_at = now()
WHERE id = sqlc.arg(subscription_id);

-- name: ListSubscriptionsToMove :many
SELECT id, slot, label, status FROM subscriptions
WHERE user_id = $1 ORDER BY slot;

-- Everything below is a plain reassignment: one statement per table, called in
-- order inside one transaction.
--
-- They are listed explicitly rather than generated from the foreign-key
-- catalogue, and the tables that are *not* here are the reason. A trial claim, a
-- Remnawave mapping, a referral attribution, and a subscription slot each need a
-- decision; a loop over every key referencing `users` would have moved them
-- without anybody making one.

-- name: MoveCustomerEntitlements :exec
UPDATE entitlements SET user_id = sqlc.arg(target), updated_at = now()
WHERE user_id = sqlc.arg(source);

-- name: MoveCustomerOrders :exec
UPDATE orders SET user_id = sqlc.arg(target), updated_at = now()
WHERE user_id = sqlc.arg(source);

-- name: MoveCustomerTickets :exec
UPDATE support_tickets SET user_id = sqlc.arg(target)
WHERE user_id = sqlc.arg(source);

-- name: MoveCustomerIdentities :exec
UPDATE identities SET user_id = sqlc.arg(target)
WHERE user_id = sqlc.arg(source);

-- name: MoveCustomerContacts :exec
UPDATE contact_channels SET user_id = sqlc.arg(target)
WHERE user_id = sqlc.arg(source);

-- name: MoveCustomerConsents :exec
UPDATE consent_records SET user_id = sqlc.arg(target)
WHERE user_id = sqlc.arg(source);

-- name: MoveCustomerTopUps :exec
UPDATE wallet_topups SET user_id = sqlc.arg(target)
WHERE user_id = sqlc.arg(source);

-- name: MoveCustomerGoodsOrders :exec
UPDATE goods_orders SET user_id = sqlc.arg(target)
WHERE user_id = sqlc.arg(source);

-- name: MoveCustomerPaymentMethods :exec
-- Saved methods move, but none of them arrives as the default.
--
-- One default per customer is a unique index, so two defaults would fail the
-- merge. The target keeps theirs, which is also the right answer: it is the
-- method they last chose, on the account that is surviving.
UPDATE payment_methods SET user_id = sqlc.arg(target), is_default = false
WHERE user_id = sqlc.arg(source);

-- name: CancelMergedCustomerCart :exec
-- A saved cart is not moved, it is cancelled.
--
-- Only one cart may be open per customer, so moving the source's would collide
-- with the target's whenever both had one — and a cart is a selection made in a
-- session that is now ending, priced against a plan version that re-quotes
-- before any charge anyway. Cancelling it is honest; silently discarding the
-- target's to make room would not be.
UPDATE carts SET status = 'cancelled', updated_at = now()
WHERE user_id = sqlc.arg(source) AND status = 'open';

-- name: MoveCustomerLifecycleEvents :exec
UPDATE customer_lifecycle_events SET user_id = sqlc.arg(target)
WHERE user_id = sqlc.arg(source);

-- name: MoveCustomerNewsReads :exec
-- Read markers move only where the target has not read the same post.
--
-- The pair is the primary key, so moving a duplicate would fail the whole
-- merge over a read receipt. The target's own marker is the one that survives:
-- both mean the same person read it.
UPDATE news_reads SET user_id = sqlc.arg(target)
WHERE news_reads.user_id = sqlc.arg(source)
  AND NOT EXISTS (
    SELECT 1 FROM news_reads existing
    WHERE existing.user_id = sqlc.arg(target) AND existing.post_id = news_reads.post_id
  );

-- name: DropDuplicateNewsReads :exec
-- Whatever the move above left behind, which is exactly the duplicates.
DELETE FROM news_reads WHERE user_id = sqlc.arg(source);

-- name: MoveCustomerSecurityEvents :exec
UPDATE customer_security_events SET user_id = sqlc.arg(target)
WHERE user_id = sqlc.arg(source);

-- name: CloseMergedAccount :one
-- Marks the source and points it at the target.
--
-- The predicate is what makes the whole operation idempotent: only an account
-- that has not already been merged matches, so a second attempt moves nothing
-- and reports the merge that already happened.
UPDATE users
SET status = 'merged', merged_into = sqlc.arg(target), merged_at = now(), updated_at = now()
WHERE id = sqlc.arg(source)
  AND status <> 'merged'
  AND merged_into IS NULL
RETURNING *;
