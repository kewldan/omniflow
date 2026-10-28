-- Customer web panel: referral, loyalty, contacts, and personal data.
--
-- Every statement here is scoped to the calling customer's identifier. That is
-- the whole authorization model of the surface, so ownership lives in the
-- WHERE clause rather than in a filter applied afterwards.
--
-- Reads that an operator surface already owns are not repeated here:
-- the referral programme, the enabled loyalty programme and its ladder, the
-- customer's standing and history, identities, orders, the wallet ledger, the
-- referral code, consent and lifecycle inserts, and the audit writer are all
-- reused from the files that define them.

-- ---------------------------------------------------------------------------
-- Referral
-- ---------------------------------------------------------------------------

-- name: AttributeAccountReferral :one
-- Decides every attribution outcome and writes the row when none applies, in
-- one statement, so two tabs racing on the same link produce one attribution
-- and the same answer for both. The insert is first-write-wins on the invited
-- customer.
WITH owner AS (
  SELECT rc.user_id FROM referral_codes rc WHERE rc.code = sqlc.arg(code)
), state AS (
  SELECT
    EXISTS (SELECT 1 FROM owner) AS owner_known,
    EXISTS (SELECT 1 FROM owner WHERE owner.user_id = sqlc.arg(user_id)::uuid) AS is_self,
    EXISTS (SELECT 1 FROM referral_attributions a WHERE a.referred_user_id = sqlc.arg(user_id)::uuid) AS already,
    EXISTS (
      SELECT 1 FROM orders o
      WHERE o.user_id = sqlc.arg(user_id)::uuid
        AND o.state IN ('paid', 'fulfilled', 'partially_refunded', 'refunded')
    ) AS has_paid
), written AS (
  INSERT INTO referral_attributions (referred_user_id, referrer_user_id, code)
  SELECT sqlc.arg(user_id)::uuid, owner.user_id, sqlc.arg(code)
  FROM owner, state
  WHERE NOT state.is_self AND NOT state.already AND NOT state.has_paid
  ON CONFLICT (referred_user_id) DO NOTHING
  RETURNING referred_user_id
)
SELECT state.owner_known, state.is_self, state.already, state.has_paid,
  EXISTS (SELECT 1 FROM written) AS inserted
FROM state;

-- name: OpenAccountReferralSummary :one
-- Ensures the customer's code exists and takes the counts against it in one
-- statement. The upsert is first-write-wins on `user_id`, so two tabs opening
-- the screen at once still produce one code; the no-op update is what makes
-- RETURNING yield the existing row.
--
-- Qualified, rejected, and pending are each counted from the rows rather than
-- derived by subtraction, because a pair can be qualified and later rejected.
WITH mine AS (
  INSERT INTO referral_codes (user_id, code) VALUES (sqlc.arg(user_id)::uuid, sqlc.arg(code))
  ON CONFLICT (user_id) DO UPDATE SET user_id = EXCLUDED.user_id
  RETURNING user_id, code
)
SELECT mine.code,
  (SELECT count(*) FROM referral_attributions a
    WHERE a.referrer_user_id = mine.user_id) AS invited,
  (SELECT count(*) FROM referral_attributions a
    WHERE a.referrer_user_id = mine.user_id AND a.qualified_at IS NOT NULL) AS qualified,
  (SELECT count(*) FROM referral_attributions a
    WHERE a.referrer_user_id = mine.user_id AND a.review_state = 'rejected') AS rejected,
  (SELECT count(*) FROM referral_attributions a
    WHERE a.referrer_user_id = mine.user_id
      AND a.qualified_at IS NULL AND a.review_state <> 'rejected') AS pending,
  (SELECT COALESCE(sum(w.amount_minor), 0) FROM referral_rewards w
    WHERE w.beneficiary_user_id = mine.user_id AND w.reversed_at IS NULL)::bigint AS rewarded_minor,
  (SELECT COALESCE(sum(w.amount_minor), 0) FROM referral_rewards w
    WHERE w.beneficiary_user_id = mine.user_id AND w.reversed_at IS NOT NULL)::bigint AS reversed_minor,
  (SELECT count(*)::integer FROM referral_rewards w
    WHERE w.beneficiary_user_id = mine.user_id
      AND w.role = 'inviter' AND w.reversed_at IS NULL) AS reward_count
FROM mine;

-- name: ListAccountReferralRewards :many
-- One keyset page of the customer's reward history, newest first. The cursor
-- is (granted_at, id); a forged one selects nothing because ownership is the
-- beneficiary predicate. The invited customer's identifier is not projected.
SELECT w.id, w.role, w.amount_minor, w.currency, w.granted_at, w.reversed_at,
  COALESCE(a.review_state, 'clear') AS review_state
FROM referral_rewards w
LEFT JOIN referral_attributions a ON a.referred_user_id = w.referred_user_id
WHERE w.beneficiary_user_id = sqlc.arg(user_id)
  AND (
    sqlc.narg(cursor_granted_at)::timestamptz IS NULL
    OR (w.granted_at, w.id) < (sqlc.narg(cursor_granted_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
ORDER BY w.granted_at DESC, w.id DESC
LIMIT sqlc.arg(page_size);

-- ---------------------------------------------------------------------------
-- Contacts
-- ---------------------------------------------------------------------------

-- name: ListAccountContacts :many
-- The customer's active channels with their sealed values, which the service
-- opens for their owner. Revoked channels are history, not something the
-- customer can be reached on.
SELECT id, kind, value_ciphertext, verified_at,
  transactional_enabled, marketing_enabled, created_at
FROM contact_channels
WHERE user_id = sqlc.arg(user_id) AND revoked_at IS NULL
ORDER BY created_at DESC, id DESC;

-- name: AddAccountContact :one
-- Registers a channel. A fingerprint already held by this customer is
-- un-revoked and re-flagged; one held by anybody else matches the conflict
-- target but not the WHERE, so no row comes back and the caller reports a
-- conflict without saying whose it is.
INSERT INTO contact_channels (
  user_id, kind, value_ciphertext, value_fingerprint,
  transactional_enabled, marketing_enabled
) VALUES (
  sqlc.arg(user_id), sqlc.arg(kind), sqlc.arg(value_ciphertext), sqlc.arg(value_fingerprint),
  sqlc.arg(transactional_enabled), sqlc.arg(marketing_enabled)
)
ON CONFLICT (kind, value_fingerprint) DO UPDATE
  SET value_ciphertext = EXCLUDED.value_ciphertext,
      transactional_enabled = EXCLUDED.transactional_enabled,
      marketing_enabled = EXCLUDED.marketing_enabled,
      revoked_at = NULL
  WHERE contact_channels.user_id = sqlc.arg(user_id)
RETURNING id, created_at;

-- name: RevokeAccountContact :one
-- Marks a channel revoked rather than deleting it, so the fingerprint stays
-- unique and the consent history keeps pointing at something.
UPDATE contact_channels SET revoked_at = now()
WHERE id = sqlc.arg(channel_id) AND user_id = sqlc.arg(user_id) AND revoked_at IS NULL
RETURNING kind, marketing_enabled;

-- name: CountAccountMarketingContacts :one
-- How many active channels still carry marketing consent. A withdrawal is
-- recorded only when this reaches zero.
SELECT count(*) FROM contact_channels
WHERE user_id = sqlc.arg(user_id) AND revoked_at IS NULL AND marketing_enabled;

-- ---------------------------------------------------------------------------
-- Privacy
-- ---------------------------------------------------------------------------

-- name: GetAccountDeletionState :one
-- The customer's most recent deletion request with the two instants that can
-- close it: the latest withdrawal, recorded as an audit event, and the latest
-- execution, recorded as a lifecycle event. Both tables are append-only, so
-- "pending" is derived from the order of the three rather than read from a
-- flag. No row means no request was ever made.
SELECT
  request.occurred_at, request.reason,
  (SELECT max(a.occurred_at) FROM audit_events a
    WHERE a.actor_type = 'customer' AND a.target_type = 'customer'
      AND a.target_id = sqlc.arg(customer_id)::text
      AND a.action = sqlc.arg(cancel_action)::text)::timestamptz AS cancelled_at,
  (SELECT max(e.occurred_at) FROM customer_lifecycle_events e
    WHERE e.user_id = sqlc.arg(user_id)::uuid
      AND e.action IN ('deleted', 'anonymized'))::timestamptz AS completed_at
FROM (
  SELECT occurred_at, reason FROM customer_lifecycle_events
  WHERE user_id = sqlc.arg(user_id)::uuid AND action = 'deletion_requested' AND actor_type = 'customer'
  ORDER BY occurred_at DESC LIMIT 1
) request;

-- name: ListAccountConsentHistory :many
-- The dated consent trail, newest first, so the first decision seen for a
-- purpose is the current one.
SELECT purpose, granted, policy_version, source, occurred_at
FROM consent_records
WHERE user_id = sqlc.arg(user_id)
ORDER BY occurred_at DESC
LIMIT sqlc.arg(page_size);

-- ---------------------------------------------------------------------------
-- Personal-data export
-- ---------------------------------------------------------------------------

-- name: ListAccountExportContacts :many
-- Every channel the installation holds, revoked ones included: having held an
-- address is itself a fact the customer is entitled to.
SELECT kind, value_ciphertext, verified_at,
  transactional_enabled, marketing_enabled, created_at, revoked_at
FROM contact_channels
WHERE user_id = sqlc.arg(user_id)
ORDER BY created_at;

-- name: ListAccountExportSubscriptions :many
-- Subscription slots without the upstream user identifier or access link.
SELECT id, slot, label, status, created_at, closed_at
FROM subscriptions
WHERE user_id = sqlc.arg(user_id)
ORDER BY slot;

-- name: ListAccountExportEntitlements :many
SELECT e.id, p.code AS plan_code, e.status, e.starts_at, e.ends_at,
  e.traffic_allowance_bytes, e.device_limit, e.created_at
FROM entitlements e
JOIN plan_versions v ON v.id = e.plan_version_id
JOIN plans p ON p.id = v.plan_id
WHERE e.user_id = sqlc.arg(user_id)
ORDER BY e.starts_at DESC
LIMIT sqlc.arg(page_size);

-- name: ListAccountExportPayments :many
-- The join to `orders` is what scopes this to the customer: `payment_intents`
-- has no customer column of its own. The provider's handles are selected so the
-- redaction is a tested Go function rather than an omission in this list.
SELECT i.order_id, i.provider, i.status, i.amount_minor, i.currency,
  i.provider_reference, i.checkout_url, i.created_at, i.updated_at
FROM payment_intents i
JOIN orders o ON o.id = i.order_id
WHERE o.user_id = sqlc.arg(user_id)
ORDER BY i.created_at DESC
LIMIT sqlc.arg(page_size);

-- name: ListAccountExportSupport :many
-- Tickets and their messages in one pass, ordered so a ticket's messages are
-- contiguous. A ticket with no messages yields one row with NULL message
-- columns. Internal operator notes live in another table this never touches.
SELECT t.id, t.status, t.created_at, t.updated_at,
  m.sender, m.body, m.created_at AS message_created_at
FROM support_tickets t
LEFT JOIN support_messages m ON m.ticket_id = t.id
WHERE t.user_id = sqlc.arg(user_id)
ORDER BY t.created_at DESC, m.created_at
LIMIT sqlc.arg(page_size);

-- name: CountAccountInvitations :one
SELECT
  (SELECT count(*) FROM referral_attributions a
    WHERE a.referrer_user_id = sqlc.arg(user_id)::uuid) AS invited,
  (SELECT count(*) FROM referral_attributions a
    WHERE a.referrer_user_id = sqlc.arg(user_id)::uuid AND a.qualified_at IS NOT NULL) AS qualified;

-- name: GetAccountInvitedBy :one
-- The invite this customer arrived through. The referrer and code are selected
-- only so the Go projection can be shown to drop them.
SELECT referrer_user_id, code, created_at, qualified_at
FROM referral_attributions
WHERE referred_user_id = sqlc.arg(user_id);

-- name: ListAccountExportReferralRewards :many
SELECT w.role, w.amount_minor, w.currency, w.granted_at, w.reversed_at,
  COALESCE(a.review_state, 'clear') AS review_state
FROM referral_rewards w
LEFT JOIN referral_attributions a ON a.referred_user_id = w.referred_user_id
WHERE w.beneficiary_user_id = sqlc.arg(user_id)
ORDER BY w.granted_at DESC
LIMIT sqlc.arg(page_size);

-- name: ListAccountExportLifecycle :many
-- The acting operator's identifier is deliberately not selected.
SELECT action, reason, actor_type, occurred_at
FROM customer_lifecycle_events
WHERE user_id = sqlc.arg(user_id)
ORDER BY occurred_at DESC
LIMIT sqlc.arg(page_size);
