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

-- name: LockMergeAccounts :many
-- Both accounts, locked for the rest of the merge, in identifier order so two
-- merges of the same pair in opposite directions queue instead of deadlocking.
--
-- The lock is FOR UPDATE rather than FOR NO KEY UPDATE on purpose. Every row
-- that names a customer — a session, a ledger entry, an identity, an order —
-- takes a key-share lock on that customer while it is written, and only a full
-- row lock conflicts with it. A sign-in minting a session, a payment crediting
-- the wallet, or a bot tap linking an identity therefore either finishes before
-- the merge reads anything or waits until the merge has committed, and never
-- lands on the absorbed account after its balance was read.
SELECT id, status FROM users
WHERE id = ANY(sqlc.arg(ids)::uuid[])
ORDER BY id
FOR UPDATE;

-- name: MergeIdentityConflicts :many
-- Providers both accounts hold an active sign-in through.
--
-- A customer holds one active sign-in per provider, so two different Telegram
-- accounts — or two different Google accounts — cannot both land on the
-- survivor. Which one should stay is the operator's call, made by unlinking the
-- other first, and not something a merge guesses.
SELECT DISTINCT s.provider
FROM identities s
JOIN identities t ON t.provider = s.provider AND t.status = 'active'
WHERE s.user_id = sqlc.arg(source) AND s.status = 'active'
  AND t.user_id = sqlc.arg(target)
ORDER BY s.provider;

-- name: MergeOpenWithdrawals :one
-- A referral payout the source has asked for and nobody has paid or refused.
--
-- The payout holds part of the source's wallet, and a refusal releases it back
-- to the account that asked. After a merge that account is closed and the
-- release would land where nobody can spend it.
SELECT count(*)::bigint FROM referral_withdrawals
WHERE user_id = sqlc.arg(source) AND status IN ('pending', 'approved');

-- name: MergeExpiringCredits :many
-- What part of the source's wallet is still due to expire, and when.
--
-- The same arithmetic the expiry job uses: a credit's unspent part is whatever
-- of it the balance still holds once the credits that came after it are set
-- aside. Moving the balance as one plain credit would turn a bonus with an end
-- date into money that never expires.
SELECT e.currency, e.expires_at,
  sum(GREATEST(
    LEAST(
      e.amount_minor,
      (SELECT COALESCE(sum(balance_entry.amount_minor), 0)
       FROM ledger_entries balance_entry
       WHERE balance_entry.account_type = 'customer_wallet'
         AND balance_entry.user_id = e.user_id
         AND balance_entry.currency = e.currency)
      - (SELECT COALESCE(sum(later_credit.amount_minor), 0)
         FROM ledger_entries later_credit
         WHERE later_credit.account_type = 'customer_wallet'
           AND later_credit.user_id = e.user_id
           AND later_credit.currency = e.currency
           AND later_credit.amount_minor > 0
           AND (later_credit.created_at, later_credit.id) > (e.created_at, e.id))
    ),
    0
  ))::bigint AS remaining_minor
FROM ledger_entries e
WHERE e.account_type = 'customer_wallet' AND e.user_id = sqlc.arg(source)
  AND e.amount_minor > 0 AND e.expires_at > now()
GROUP BY e.currency, e.expires_at
ORDER BY e.currency, e.expires_at;

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

-- name: MoveCustomerLegacyPayments :exec
-- Payments carried over from the previous bot follow the customer, as orders
-- do, so the merged account keeps the spend its segments and tiers read.
UPDATE legacy_payments SET user_id = sqlc.arg(target)
WHERE user_id = sqlc.arg(source);

-- name: MoveCustomerLegacyRecords :exec
-- The previous bot's history follows the customer too.
UPDATE legacy_records SET user_id = sqlc.arg(target)
WHERE user_id = sqlc.arg(source);

-- name: MoveCustomerLegacyPeriods :exec
UPDATE legacy_subscription_periods SET user_id = sqlc.arg(target), updated_at = now()
WHERE user_id = sqlc.arg(source);

-- name: MoveCustomerReferralAliases :exec
-- Invite links the source handed out in the previous bot keep crediting the
-- person, now the merged account.
UPDATE referral_code_aliases SET user_id = sqlc.arg(target)
WHERE user_id = sqlc.arg(source);

-- name: MoveCustomerLegacyDiscount :exec
-- The later of the two customers' carried discount dates survives, so the
-- merged account is no easier to discount than either was.
WITH moved AS (
  DELETE FROM legacy_customer_discounts gone WHERE gone.user_id = sqlc.arg(source)::uuid
  RETURNING gone.source AS origin, gone.last_discount_at
)
INSERT INTO legacy_customer_discounts (user_id, source, last_discount_at)
SELECT sqlc.arg(target)::uuid, moved.origin, moved.last_discount_at FROM moved
ON CONFLICT (user_id) DO UPDATE
SET last_discount_at = GREATEST(legacy_customer_discounts.last_discount_at, EXCLUDED.last_discount_at),
    updated_at = now();

-- name: CarryMergedPrimaryRemnawaveUser :exec
-- The source's primary subscription takes the Remnawave user the customer-level
-- mapping names, before it is renumbered onto the survivor.
--
-- Fulfillment reads the mapping only for slot 1. A subscription that relied on
-- it and arrives on the survivor in slot 2 would otherwise look unprovisioned,
-- and the next renewal would create a second VPN user with a new link while
-- the one the customer has configured keeps running out.
UPDATE subscriptions s
SET remnawave_user_id = m.remnawave_id, updated_at = now()
FROM remnawave_users m
WHERE s.user_id = sqlc.arg(source) AND s.slot = 1 AND s.remnawave_user_id IS NULL
  AND m.user_id = sqlc.arg(source)
  AND NOT EXISTS (SELECT 1 FROM subscriptions other WHERE other.remnawave_user_id = m.remnawave_id);

-- name: AdoptMergedRemnawaveUser :exec
-- A source imported from Remnawave that never bought anything holds a VPN user
-- through the mapping alone. It becomes a subscription of its own, the way the
-- bot adopts an imported user, so the survivor keeps it as a separate link
-- instead of the mapping being left behind on a closed account.
INSERT INTO subscriptions (user_id, slot, label, remnawave_user_id, reconciled_at)
SELECT sqlc.arg(source), 1, 'Subscription 1', m.remnawave_id, now()
FROM remnawave_users m
WHERE m.user_id = sqlc.arg(source)
  AND NOT EXISTS (SELECT 1 FROM subscriptions s WHERE s.user_id = sqlc.arg(source))
  AND NOT EXISTS (SELECT 1 FROM subscriptions other WHERE other.remnawave_user_id = m.remnawave_id);

-- name: MoveCustomerRemnawaveMapping :exec
-- The customer-level mapping moves when the survivor has none. When it has its
-- own, the source's stays behind — every Remnawave user it named now belongs
-- to a subscription — but gives up its Telegram ID, so the bot and the
-- notification queries that find a customer by Telegram ID reach the survivor.
WITH survivor AS (
  SELECT 1 AS present FROM remnawave_users mine WHERE mine.user_id = sqlc.arg(target)
), moved AS (
  UPDATE remnawave_users SET user_id = sqlc.arg(target)
  WHERE remnawave_users.user_id = sqlc.arg(source) AND NOT EXISTS (SELECT 1 FROM survivor)
  RETURNING 1 AS done
)
UPDATE remnawave_users SET telegram_id = NULL
WHERE remnawave_users.user_id = sqlc.arg(source) AND EXISTS (SELECT 1 FROM survivor);

-- name: CarryMergedTelegramMapping :exec
-- The Telegram ID the source's mapping gave up, on the survivor's mapping when
-- that has none.
UPDATE remnawave_users SET telegram_id = sqlc.arg(telegram_id)
WHERE remnawave_users.user_id = sqlc.arg(target) AND telegram_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM remnawave_users other WHERE other.telegram_id = sqlc.arg(telegram_id));

-- name: MergeSourceTelegramMapping :one
SELECT COALESCE(telegram_id, 0)::bigint FROM remnawave_users WHERE user_id = sqlc.arg(source);

-- name: RevokeMergedCustomerSessions :many
-- Every session of the absorbed account ends. The person signs in again and
-- lands on the survivor, which now holds their sign-in methods.
UPDATE customer_sessions
SET revoked_at = now(), revoked_reason = 'account_merged'
WHERE user_id = sqlc.arg(source) AND revoked_at IS NULL
RETURNING 1 AS done;

-- name: DropAbandonedTrialClaim :exec
-- A trial claim on the survivor whose order closed unpaid is a claim that never
-- happened, and ClaimTrial would let it be taken over. When the source has a
-- claim that stands, that one is what the person has used.
DELETE FROM trial_claims t
WHERE t.user_id = sqlc.arg(target)
  AND EXISTS (SELECT 1 FROM trial_claims s WHERE s.user_id = sqlc.arg(source))
  AND EXISTS (
    SELECT 1 FROM orders closed
    WHERE closed.id = t.order_id AND closed.state IN ('cancelled', 'expired')
  );

-- name: MoveCustomerTrialClaim :exec
-- One trial per person. A trial the source took counts against the survivor,
-- or a merge would hand out a second one.
UPDATE trial_claims SET user_id = sqlc.arg(target)
WHERE trial_claims.user_id = sqlc.arg(source)
  AND NOT EXISTS (SELECT 1 FROM trial_claims t WHERE t.user_id = sqlc.arg(target));

-- name: MoveCustomerReferralsMade :exec
-- The people the source invited stay invited by the person. Their commissions
-- would otherwise keep landing in the wallet of a closed account.
UPDATE referral_attributions SET referrer_user_id = sqlc.arg(target)
WHERE referrer_user_id = sqlc.arg(source) AND referred_user_id <> sqlc.arg(target);

-- name: MoveCustomerReferralAttribution :exec
-- Who invited the source follows the person when the survivor was not invited
-- by anybody, with the rewards already paid for it, so a first-payment reward
-- is not paid a second time for the same signup. A survivor with an inviter of
-- its own keeps that one; the source's stays where it was, and nothing more is
-- paid on it because the closed account buys nothing.
WITH moved AS (
  UPDATE referral_attributions SET referred_user_id = sqlc.arg(target)
  WHERE referred_user_id = sqlc.arg(source)
    AND referrer_user_id <> sqlc.arg(target)
    AND NOT EXISTS (SELECT 1 FROM referral_attributions t WHERE t.referred_user_id = sqlc.arg(target))
  RETURNING referred_user_id
)
UPDATE referral_rewards r SET referred_user_id = sqlc.arg(target)
WHERE r.referred_user_id = sqlc.arg(source)
  AND EXISTS (SELECT 1 FROM moved)
  AND (r.role = 'commission' OR NOT EXISTS (
    SELECT 1 FROM referral_rewards t WHERE t.referred_user_id = sqlc.arg(target) AND t.role = r.role
  ));

-- name: MoveCustomerReferralBeneficiary :exec
-- Rewards the source earned point at the survivor, so a reversal after a
-- refund debits the wallet the reward now sits in.
UPDATE referral_rewards SET beneficiary_user_id = sqlc.arg(target)
WHERE beneficiary_user_id = sqlc.arg(source);

-- name: MoveCustomerLevel2Rewards :exec
UPDATE referral_level2_rewards SET beneficiary_user_id = sqlc.arg(target)
WHERE beneficiary_user_id = sqlc.arg(source);

-- name: MoveCustomerReferralCode :exec
-- The source's invite code keeps working for the person. A survivor without a
-- code takes it over; one with a code of its own gets it as an alias.
UPDATE referral_codes SET user_id = sqlc.arg(target)
WHERE referral_codes.user_id = sqlc.arg(source)
  AND NOT EXISTS (SELECT 1 FROM referral_codes t WHERE t.user_id = sqlc.arg(target));

-- name: AliasMergedReferralCode :exec
WITH gone AS (
  DELETE FROM referral_codes WHERE referral_codes.user_id = sqlc.arg(source) RETURNING code
)
INSERT INTO referral_code_aliases (code, user_id, source)
SELECT code, sqlc.arg(target), 'merge' FROM gone
ON CONFLICT (code) DO NOTHING;

-- name: MoveCustomerReferralTerms :exec
-- Individual partner terms and the reward preference follow the person unless
-- the survivor already has its own.
WITH terms AS (
  UPDATE referral_inviter_terms SET user_id = sqlc.arg(target)
  WHERE referral_inviter_terms.user_id = sqlc.arg(source)
    AND NOT EXISTS (SELECT 1 FROM referral_inviter_terms t WHERE t.user_id = sqlc.arg(target))
  RETURNING 1 AS done
)
UPDATE referral_reward_preferences SET user_id = sqlc.arg(target)
WHERE referral_reward_preferences.user_id = sqlc.arg(source)
  AND NOT EXISTS (SELECT 1 FROM referral_reward_preferences t WHERE t.user_id = sqlc.arg(target));

-- name: MoveCustomerPromoRedemptions :exec
-- Redemptions count against a promotion's per-customer limit. Left behind,
-- they would let the person use a once-per-customer code a second time.
UPDATE promo_redemptions SET user_id = sqlc.arg(target)
WHERE promo_redemptions.user_id = sqlc.arg(source);

-- name: MoveCustomerPersonalOffers :exec
-- A live offer for a promotion the survivor already has a live offer for stays
-- behind: one live offer per promotion per customer.
UPDATE personal_offers p SET user_id = sqlc.arg(target)
WHERE p.user_id = sqlc.arg(source)
  AND (p.status <> 'active' OR NOT EXISTS (
    SELECT 1 FROM personal_offers t
    WHERE t.user_id = sqlc.arg(target) AND t.promotion_id = p.promotion_id AND t.status = 'active'
  ));

-- name: MoveCustomerGifts :exec
-- Gifts the source sent are refunded to the sender if nobody claims them, and
-- gifts it claimed are part of its history.
WITH sent AS (
  UPDATE gifts SET sender_user_id = sqlc.arg(target), updated_at = now()
  WHERE sender_user_id = sqlc.arg(source)
  RETURNING 1 AS done
), received AS (
  UPDATE gifts SET recipient_user_id = sqlc.arg(target), updated_at = now()
  WHERE recipient_user_id = sqlc.arg(source)
  RETURNING 1 AS done
)
UPDATE gift_recipients SET recipient_user_id = sqlc.arg(target)
WHERE gift_recipients.recipient_user_id = sqlc.arg(source);

-- name: MoveCustomerLegacyGoodsOrders :exec
UPDATE legacy_goods_orders SET user_id = sqlc.arg(target)
WHERE legacy_goods_orders.user_id = sqlc.arg(source);

-- name: MoveCustomerBedolagaPaymentClaims :exec
UPDATE bedolaga_payment_claims SET user_id = sqlc.arg(target)
WHERE bedolaga_payment_claims.user_id = sqlc.arg(source);

-- name: MoveCustomerAttribution :exec
-- One acquisition record per customer, campaign and first visit alike. The
-- survivor's own stands; the source's moves only when the survivor has none,
-- carrying whether its campaign bonus was already paid.
WITH campaign AS (
  UPDATE customer_campaign_attributions SET user_id = sqlc.arg(target)
  WHERE customer_campaign_attributions.user_id = sqlc.arg(source)
    AND NOT EXISTS (SELECT 1 FROM customer_campaign_attributions t WHERE t.user_id = sqlc.arg(target))
  RETURNING 1 AS done
)
UPDATE customer_start_attributions SET user_id = sqlc.arg(target)
WHERE customer_start_attributions.user_id = sqlc.arg(source)
  AND NOT EXISTS (SELECT 1 FROM customer_start_attributions t WHERE t.user_id = sqlc.arg(target));

-- name: MoveCustomerLoyalty :exec
-- Tier standing is recomputed from spend, which the merge has just moved, so
-- the source's standing only fills an empty slot. Tiers an operator or a promo
-- code granted are kept where the survivor does not already hold the same one.
WITH standing AS (
  UPDATE loyalty_standings SET user_id = sqlc.arg(target), updated_at = now()
  WHERE loyalty_standings.user_id = sqlc.arg(source)
    AND NOT EXISTS (SELECT 1 FROM loyalty_standings t WHERE t.user_id = sqlc.arg(target))
  RETURNING 1 AS done
), history AS (
  UPDATE loyalty_standing_history SET user_id = sqlc.arg(target)
  WHERE loyalty_standing_history.user_id = sqlc.arg(source)
  RETURNING 1 AS done
)
UPDATE loyalty_memberships m SET user_id = sqlc.arg(target)
WHERE m.user_id = sqlc.arg(source)
  AND NOT EXISTS (
    SELECT 1 FROM loyalty_memberships t WHERE t.user_id = sqlc.arg(target) AND t.tier_code = m.tier_code
  );

-- name: MoveCustomerRestrictions :exec
-- Restrictions and communication opt-outs follow the person. A merge must not
-- be a way to shed a purchase ban, and a customer who asked not to be written
-- to has not changed their mind by gaining a second account.
WITH restrictions AS (
  UPDATE customer_restrictions r SET user_id = sqlc.arg(target)
  WHERE r.user_id = sqlc.arg(source)
    AND NOT EXISTS (
      SELECT 1 FROM customer_restrictions t WHERE t.user_id = sqlc.arg(target) AND t.kind = r.kind
    )
  RETURNING 1 AS done
), suppressions AS (
  UPDATE communication_suppressions SET user_id = sqlc.arg(target)
  WHERE communication_suppressions.user_id = sqlc.arg(source)
    AND NOT EXISTS (SELECT 1 FROM communication_suppressions t WHERE t.user_id = sqlc.arg(target))
  RETURNING 1 AS done
), exemptions AS (
  UPDATE channel_exemptions SET user_id = sqlc.arg(target)
  WHERE channel_exemptions.user_id = sqlc.arg(source)
    AND NOT EXISTS (SELECT 1 FROM channel_exemptions t WHERE t.user_id = sqlc.arg(target))
  RETURNING 1 AS done
)
UPDATE blocklist_allowlist SET user_id = sqlc.arg(target)
WHERE blocklist_allowlist.user_id = sqlc.arg(source)
  AND NOT EXISTS (SELECT 1 FROM blocklist_allowlist t WHERE t.user_id = sqlc.arg(target));

-- name: MoveCustomerAutoRenew :exec
-- Auto-renewal of a moved subscription moves with it, so the renewal charges
-- the wallet the balance moved to. A customer-wide setting moves when the
-- survivor has none; otherwise the source's is switched off rather than left
-- to charge a closed account.
WITH moved AS (
  UPDATE auto_renew_settings a SET user_id = sqlc.arg(target), updated_at = now()
  WHERE a.user_id = sqlc.arg(source)
    AND NOT EXISTS (
      SELECT 1 FROM auto_renew_settings t
      WHERE t.user_id = sqlc.arg(target) AND t.subscription_id IS NOT DISTINCT FROM a.subscription_id
    )
  RETURNING a.subscription_id
)
UPDATE auto_renew_settings SET enabled = false, state = 'idle', cancelled_at = now(), updated_at = now()
WHERE auto_renew_settings.user_id = sqlc.arg(source) AND enabled
  AND subscription_id IS NOT DISTINCT FROM NULL
  AND NOT EXISTS (SELECT 1 FROM moved WHERE moved.subscription_id IS NULL);

-- name: MoveCustomerProfileRows :exec
-- The bot's preferences and the last Telegram username seen fill the
-- survivor's only where it has none; a username is unique, so a duplicate the
-- survivor does not take is dropped rather than left to be found on the closed
-- account.
WITH preferences AS (
  UPDATE bot_preferences SET user_id = sqlc.arg(target)
  WHERE bot_preferences.user_id = sqlc.arg(source)
    AND NOT EXISTS (SELECT 1 FROM bot_preferences t WHERE t.user_id = sqlc.arg(target))
  RETURNING 1 AS done
), username AS (
  UPDATE telegram_usernames SET user_id = sqlc.arg(target)
  WHERE telegram_usernames.user_id = sqlc.arg(source)
    AND NOT EXISTS (SELECT 1 FROM telegram_usernames t WHERE t.user_id = sqlc.arg(target))
  RETURNING 1 AS done
)
DELETE FROM telegram_usernames
WHERE telegram_usernames.user_id = sqlc.arg(source) AND NOT EXISTS (SELECT 1 FROM username);

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
