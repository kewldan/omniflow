-- name: GetCustomer :one
SELECT * FROM users WHERE id = $1;

-- name: UpdateCustomerPreferences :one
UPDATE users
SET locale = sqlc.arg(locale), timezone = sqlc.arg(timezone), updated_at = now()
WHERE id = sqlc.arg(user_id) AND status <> 'deleted'
RETURNING *;

-- name: ApplyCustomerLifecycle :one
UPDATE users
SET status = sqlc.arg(status),
    suspended_at = sqlc.narg(suspended_at),
    deleted_at = sqlc.narg(deleted_at),
    anonymized_at = sqlc.narg(anonymized_at),
    retention_until = sqlc.narg(retention_until),
    updated_at = now()
WHERE id = sqlc.arg(user_id)
RETURNING *;

-- name: AnonymizeCustomerData :exec
WITH revoked_identities AS (
  UPDATE identities
  SET provider_subject = 'anonymized:' || id::text, status = 'revoked', revoked_at = now(), metadata = '{}'::jsonb
  WHERE identities.user_id = sqlc.arg(target_user_id)
), revoked_contacts AS (
  UPDATE contact_channels
  SET value_ciphertext = NULL, value_fingerprint = digest(id::text, 'sha256'), revoked_at = now(),
      transactional_enabled = false, marketing_enabled = false
  WHERE contact_channels.user_id = sqlc.arg(target_user_id)
), cleared_mapping AS (
  UPDATE remnawave_users SET telegram_id = NULL, observed_state = '{}'::jsonb WHERE remnawave_users.user_id = sqlc.arg(target_user_id)
), redacted_support AS (
  UPDATE support_messages SET body = '[anonymized]', telegram_message_id = NULL
  WHERE support_messages.ticket_id IN (SELECT support_tickets.id FROM support_tickets WHERE support_tickets.user_id = sqlc.arg(target_user_id))
)
DELETE FROM referral_codes WHERE referral_codes.user_id = sqlc.arg(target_user_id);

-- name: ListCustomerIdentities :many
SELECT * FROM identities WHERE user_id = $1 ORDER BY created_at;

-- name: LinkCustomerIdentity :one
INSERT INTO identities (user_id, provider, provider_subject, verified_at, status, metadata)
VALUES (sqlc.arg(user_id), sqlc.arg(provider), sqlc.arg(provider_subject), sqlc.arg(verified_at), 'active', sqlc.arg(metadata))
ON CONFLICT (provider, provider_subject) DO UPDATE
SET verified_at = COALESCE(identities.verified_at, EXCLUDED.verified_at)
WHERE identities.user_id = EXCLUDED.user_id AND identities.status = 'active'
RETURNING *;

-- name: RevokeCustomerIdentity :one
UPDATE identities
SET status = 'revoked', revoked_at = now()
WHERE id = sqlc.arg(identity_id) AND user_id = sqlc.arg(user_id) AND status = 'active'
RETURNING *;

-- name: InsertConsentRecord :one
INSERT INTO consent_records (user_id, purpose, granted, policy_version, source, request_id)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: CreateContactChannel :one
INSERT INTO contact_channels (
  user_id, kind, value_ciphertext, value_fingerprint, verified_at,
  transactional_enabled, marketing_enabled
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (kind, value_fingerprint) DO UPDATE
SET transactional_enabled = EXCLUDED.transactional_enabled,
    marketing_enabled = EXCLUDED.marketing_enabled
WHERE contact_channels.user_id = EXCLUDED.user_id AND contact_channels.revoked_at IS NULL
RETURNING *;

-- name: ListContactChannels :many
SELECT id, user_id, kind, value_fingerprint, verified_at, transactional_enabled,
       marketing_enabled, created_at, revoked_at
FROM contact_channels WHERE user_id = $1 AND revoked_at IS NULL ORDER BY created_at;

-- name: UpdateContactChannelPreferences :one
UPDATE contact_channels
SET transactional_enabled = sqlc.arg(transactional_enabled),
    marketing_enabled = sqlc.arg(marketing_enabled)
WHERE id = sqlc.arg(channel_id) AND user_id = sqlc.arg(user_id) AND revoked_at IS NULL
RETURNING *;

-- name: GetLatestConsents :many
SELECT DISTINCT ON (purpose) *
FROM consent_records
WHERE user_id = $1
ORDER BY purpose, occurred_at DESC;

-- name: InsertCustomerLifecycleEvent :one
INSERT INTO customer_lifecycle_events (user_id, action, reason, actor_type, actor_id, request_id)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: CreateCustomerImport :one
INSERT INTO customer_imports (source) VALUES ('remnawave') RETURNING *;

-- name: GetCustomerImport :one
SELECT * FROM customer_imports WHERE id = $1;

-- name: UpsertCustomerImportItem :one
INSERT INTO customer_import_items (import_id, source_id, status, fingerprint, staged_data, validation_errors)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (import_id, source_id) DO UPDATE
SET status = EXCLUDED.status,
    fingerprint = EXCLUDED.fingerprint,
    staged_data = EXCLUDED.staged_data,
    validation_errors = EXCLUDED.validation_errors
WHERE customer_import_items.status NOT IN ('applied', 'skipped')
RETURNING *;

-- name: ListCustomerImportItems :many
SELECT * FROM customer_import_items
WHERE import_id = $1 AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
ORDER BY source_id
LIMIT sqlc.arg(page_size);

-- name: ListCustomerImportTelegramIDs :many
SELECT source_id, (staged_data ->> 'telegramId')::bigint AS telegram_id
FROM customer_import_items
WHERE import_id = $1 AND staged_data ->> 'telegramId' IS NOT NULL;

-- name: GetCustomerImportItemCounts :one
SELECT count(*) FILTER (WHERE status = 'valid')::integer AS valid_count,
       count(*) FILTER (WHERE status = 'conflict')::integer AS conflict_count,
       count(*) FILTER (WHERE status = 'invalid')::integer AS invalid_count
FROM customer_import_items
WHERE import_id = $1;

-- name: ListTelegramIdentitySubjects :many
SELECT provider_subject
FROM identities
WHERE provider = 'telegram' AND status = 'active';

-- name: ListRemnawaveMappings :many
SELECT remnawave_id, telegram_id FROM remnawave_users;

-- name: ApplyCustomerImportItem :one
WITH locked_item AS (
  SELECT * FROM customer_import_items
  WHERE import_id = sqlc.arg(import_id) AND source_id = sqlc.arg(source_id) AND status = 'valid'
  FOR UPDATE
), created_user AS (
  INSERT INTO users (status, locale)
  SELECT 'active', sqlc.arg(locale) FROM locked_item
  RETURNING id
), mapping AS (
  INSERT INTO remnawave_users (user_id, remnawave_id, telegram_id, observed_state, reconciled_at)
  SELECT id, sqlc.arg(remnawave_id), sqlc.narg(telegram_id), sqlc.arg(observed_state), now()
  FROM created_user
  RETURNING user_id
)
UPDATE customer_import_items item
SET status = 'applied', user_id = mapping.user_id, applied_at = now()
FROM mapping
WHERE item.import_id = sqlc.arg(import_id) AND item.source_id = sqlc.arg(source_id)
RETURNING item.*;

-- name: UpdateCustomerImportProgress :one
UPDATE customer_imports
SET status = sqlc.arg(status), cursor = sqlc.narg(cursor), total_count = sqlc.arg(total_count),
    valid_count = sqlc.arg(valid_count), conflict_count = sqlc.arg(conflict_count),
    invalid_count = sqlc.arg(invalid_count), error_summary = sqlc.arg(error_summary), updated_at = now(),
    completed_at = CASE WHEN sqlc.arg(status) IN ('completed', 'failed', 'cancelled') THEN now() ELSE NULL END
WHERE id = sqlc.arg(import_id)
RETURNING *;

-- name: ListVisiblePlans :many
SELECT p.id, p.code, p.kind, p.sort_order, l.locale, l.name, l.description,
       v.id AS plan_version_id, v.version, v.billing_period, v.duration_seconds,
       v.traffic_allowance_bytes, v.device_limit, v.remnawave_squad_ids,
       v.recurring_capable, pr.currency, pr.amount_minor
FROM plans p
JOIN plan_localizations l ON l.plan_id = p.id AND l.locale = sqlc.arg(locale)
JOIN LATERAL (
  SELECT * FROM plan_versions pv
  WHERE pv.plan_id = p.id AND pv.retired_at IS NULL
  ORDER BY pv.version DESC LIMIT 1
) v ON true
JOIN plan_prices pr ON pr.plan_version_id = v.id
WHERE p.visible AND p.archived_at IS NULL
ORDER BY p.sort_order, p.code, pr.currency;

-- name: CreatePlan :one
INSERT INTO plans (code, kind, visible, sort_order) VALUES ($1, $2, $3, $4)
ON CONFLICT (code) DO UPDATE SET code = EXCLUDED.code
RETURNING *;

-- name: UpsertPlanLocalization :one
INSERT INTO plan_localizations (plan_id, locale, name, description)
VALUES ($1, $2, $3, $4)
ON CONFLICT (plan_id, locale) DO UPDATE SET name = EXCLUDED.name, description = EXCLUDED.description
RETURNING *;

-- name: NextPlanVersion :one
WITH locked AS (SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0)))
SELECT COALESCE(max(version), 0)::integer + 1 FROM plan_versions, locked WHERE plan_id = $1;

-- name: CreatePlanVersion :one
INSERT INTO plan_versions (
  plan_id, version, billing_period, duration_seconds, traffic_allowance_bytes,
  device_limit, remnawave_squad_ids, upgrade_policy, downgrade_policy,
  cancellation_policy, recurring_capable
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: CreatePlanPrice :one
INSERT INTO plan_prices (plan_version_id, currency, amount_minor)
VALUES ($1, $2, $3)
RETURNING *;

-- name: SetPlanVisibility :one
UPDATE plans SET visible = $2, sort_order = $3 WHERE id = $1 RETURNING *;

-- name: CreatePromotion :one
INSERT INTO promotions (
  code, kind, value, currency, starts_at, ends_at, redemption_limit,
  per_customer_limit, eligibility, active
)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
ON CONFLICT (code) DO UPDATE SET code = EXCLUDED.code
RETURNING *;

-- name: AddPromotionPlan :exec
INSERT INTO promotion_plans (promotion_id, plan_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: CreatePromoCode :one
INSERT INTO promo_codes (promotion_id, normalized_code, redemption_limit, active)
VALUES ($1,$2,$3,$4)
ON CONFLICT (normalized_code) DO UPDATE SET normalized_code = EXCLUDED.normalized_code
RETURNING *;

-- name: GetPlanVersionForOrder :one
SELECT p.code, p.kind, p.max_concurrent_per_customer, v.*, pr.currency, pr.amount_minor
FROM plan_versions v
JOIN plans p ON p.id = v.plan_id
JOIN plan_prices pr ON pr.plan_version_id = v.id
WHERE v.id = sqlc.arg(plan_version_id) AND pr.currency = sqlc.arg(currency);

-- name: GetPromoForRedemption :one
SELECT pc.*, p.kind, p.value, p.currency, p.starts_at, p.ends_at,
       p.redemption_limit AS promotion_redemption_limit, p.per_customer_limit, p.eligibility,
       p.applies_to
FROM promo_codes pc
JOIN promotions p ON p.id = pc.promotion_id
WHERE pc.normalized_code = sqlc.arg(normalized_code) AND pc.active AND p.active
FOR UPDATE OF pc, p;

-- name: CountPromoRedemptions :one
-- A redemption is written when the order is created, before any money moves,
-- so a redemption whose order closed unpaid — cancelled by the customer or
-- expired by the sweep — is a redemption that never happened and does not
-- count against any limit. A live pending order still counts: it is what
-- stops two parallel checkouts from both passing a limit of one.
SELECT count(*)::integer AS total_count,
       count(*) FILTER (WHERE r.user_id = sqlc.arg(user_id))::integer AS customer_count,
       count(*) FILTER (WHERE r.promo_code_id = sqlc.arg(promo_code_id))::integer AS code_count
FROM promo_redemptions r
JOIN orders o ON o.id = r.order_id
WHERE r.promotion_id = sqlc.arg(promotion_id)
  AND o.state NOT IN ('cancelled', 'expired');

-- name: CheckPromotionCustomerEligibility :one
-- A "new customer" is one who has never settled a subscription order. A
-- wallet top-up, a shop purchase, a gift bought for somebody else, or a code
-- a distributor paid for is not the purchase a welcome offer is about, so the
-- completed-order counts look at subscription operations only — the same
-- predicate referral qualification uses.
SELECT
  (NOT (sqlc.arg(eligibility)::jsonb ? 'locales') OR (sqlc.arg(eligibility)::jsonb -> 'locales') ? u.locale)
  AND (NOT COALESCE((sqlc.arg(eligibility)::jsonb ->> 'newCustomerOnly')::boolean, false)
       OR count(o.id) FILTER (WHERE o.state IN ('paid','fulfilled','partially_refunded','refunded')) = 0)
  AND count(o.id) FILTER (WHERE o.state IN ('paid','fulfilled','partially_refunded','refunded'))
       >= COALESCE((sqlc.arg(eligibility)::jsonb ->> 'minimumCompletedOrders')::integer, 0) AS eligible
FROM users u
LEFT JOIN orders o ON o.user_id = u.id
  AND o.operation IN ('purchase','extension','renewal','upgrade','downgrade')
WHERE u.id = sqlc.arg(user_id)
GROUP BY u.id;

-- name: IsPromotionPlanEligible :one
-- A promotion must name the plans catalogue to discount a plan.
--
-- The applies_to clause is load-bearing rather than belt-and-braces: an
-- unscoped promotion has no promotion_plans rows, so without it a promotion
-- written for the shop would pass the wildcard branch and discount every plan.
SELECT (
  EXISTS (
    SELECT 1 FROM promotions p
    WHERE p.id = sqlc.arg(target_promotion_id) AND p.applies_to = 'plans'
  )
  AND (
    NOT EXISTS (SELECT 1 FROM promotion_plans pp WHERE pp.promotion_id = sqlc.arg(target_promotion_id))
    OR EXISTS (
      SELECT 1 FROM promotion_plans pp
      WHERE pp.promotion_id = sqlc.arg(target_promotion_id) AND pp.plan_id = sqlc.arg(target_plan_id)
    )
  )
)::boolean AS eligible;

-- name: IsPromotionGoodsEligible :one
-- The same rule for the shop. Empty scoping means every visible product,
-- which is safe here in a way the applies_to default was not: an operator
-- writing a goods promotion has by definition decided goods are in scope.
SELECT (
  EXISTS (
    SELECT 1 FROM promotions p
    WHERE p.id = sqlc.arg(target_promotion_id) AND p.applies_to = 'goods'
  )
  AND (
    NOT EXISTS (SELECT 1 FROM promotion_goods pg WHERE pg.promotion_id = sqlc.arg(target_promotion_id))
    OR EXISTS (
      SELECT 1 FROM promotion_goods pg
      WHERE pg.promotion_id = sqlc.arg(target_promotion_id) AND pg.product_id = sqlc.arg(target_product_id)
    )
  )
)::boolean AS eligible;

-- name: InsertPromoRedemption :one
INSERT INTO promo_redemptions (promo_code_id, promotion_id, user_id, order_id, discount_minor)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: CreateOrder :one
INSERT INTO orders (
  user_id, state, operation, currency, subtotal_minor, discount_minor,
  wallet_minor, external_minor, idempotency_key, expires_at,
  subscription_id, selected_squad_ids
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (user_id, idempotency_key) DO UPDATE SET idempotency_key = EXCLUDED.idempotency_key
RETURNING *;

-- name: GetOrder :one
SELECT * FROM orders WHERE id = $1;

-- name: GetOrderByIdempotency :one
SELECT * FROM orders WHERE user_id = $1 AND idempotency_key = $2;

-- name: GetOrderEntitlementSpec :one
SELECT ol.plan_version_id, pv.duration_seconds, pv.traffic_allowance_bytes,
       pv.device_limit, pv.remnawave_squad_ids, pv.upgrade_policy,
       pv.downgrade_policy, pv.cancellation_policy
FROM order_lines ol
JOIN plan_versions pv ON pv.id = ol.plan_version_id
WHERE ol.order_id = $1;

-- name: GetPlanVersionGracePeriod :one
-- The grace window the fulfillment worker adds to the paid end when it pushes
-- an expiry to Remnawave. It is read per operation rather than copied into the
-- desired state, so every creator of an operation agrees on it by construction.
SELECT grace_period_seconds FROM plan_versions WHERE id = $1;

-- name: GetLatestEntitlementForChange :one
SELECT * FROM entitlements
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.narg(subscription_id)::uuid IS NULL OR subscription_id = sqlc.narg(subscription_id)::uuid)
  AND status IN ('pending', 'active', 'limited', 'disabled')
ORDER BY ends_at DESC
LIMIT 1
FOR UPDATE;

-- name: LockOrder :one
SELECT * FROM orders WHERE id = $1 FOR UPDATE;

-- name: InsertOrderLine :one
INSERT INTO order_lines (order_id, plan_id, plan_version_id, unit_amount_minor, snapshot)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (order_id, plan_version_id) DO UPDATE SET snapshot = order_lines.snapshot
RETURNING *;

-- name: UpdateOrderPayment :one
-- paid_at is set the first time the order settles and never again. COALESCE is
-- what makes that true: a partial refund moves the state through this statement
-- a second time, and a sale must not change reporting period because somebody
-- was refunded a month later.
UPDATE orders
SET state = sqlc.arg(state), paid_minor = sqlc.arg(paid_minor),
    paid_at = CASE
      WHEN sqlc.arg(state) IN ('paid', 'fulfilled', 'partially_refunded', 'refunded')
      THEN COALESCE(paid_at, now())
      ELSE paid_at
    END,
    updated_at = now()
WHERE id = sqlc.arg(order_id)
RETURNING *;

-- name: UpdateOrderRefund :one
UPDATE orders
SET state = sqlc.arg(state), refunded_minor = sqlc.arg(refunded_minor), updated_at = now()
WHERE id = sqlc.arg(order_id)
RETURNING *;

-- name: SetOrderState :one
-- The same first-settlement rule as UpdateOrderPayment. It is repeated rather
-- than centralised because these are the only two statements that can move an
-- order into a settled state, and a report keyed on a timestamp that one of
-- them forgot to set would under-count silently.
UPDATE orders
SET state = $2,
    paid_at = CASE
      WHEN $2 IN ('paid', 'fulfilled', 'partially_refunded', 'refunded')
      THEN COALESCE(paid_at, now())
      ELSE paid_at
    END,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ExtendOrderExpiry :one
-- Lengthens a pending order's payment window for a provider chosen after the
-- order was created. GREATEST keeps it from ever shortening one, the state
-- guard keeps a closed order closed, and a goods order is excluded because its
-- deadline is the gateway quote's validity rather than a payment window.
UPDATE orders
SET expires_at = GREATEST(expires_at, sqlc.arg(expires_at)::timestamptz), updated_at = now()
WHERE id = sqlc.arg(order_id) AND state = 'pending' AND operation <> 'goods'
RETURNING *;

-- name: CancelOrder :one
WITH mutation AS (
  INSERT INTO order_mutations (order_id, action, idempotency_key, reason)
  VALUES (sqlc.arg(order_id), 'cancel', sqlc.arg(idempotency_key), sqlc.arg(reason))
  ON CONFLICT (order_id, action, idempotency_key) DO UPDATE SET idempotency_key = EXCLUDED.idempotency_key
)
UPDATE orders SET state = 'cancelled', updated_at = now()
WHERE orders.id = sqlc.arg(order_id) AND orders.state IN ('draft', 'pending', 'cancelled')
RETURNING orders.*;

-- name: ExpirePendingOrders :many
WITH expired AS (
  SELECT id FROM orders WHERE state IN ('draft','pending') AND expires_at <= now() FOR UPDATE SKIP LOCKED
), mutations AS (
  INSERT INTO order_mutations (order_id, action, idempotency_key, reason)
  SELECT id, 'expire', 'system:expiry', 'payment window elapsed' FROM expired
  ON CONFLICT DO NOTHING
)
UPDATE orders SET state = 'expired', updated_at = now() WHERE id IN (SELECT id FROM expired)
RETURNING *;

-- name: CreatePaymentIntent :one
INSERT INTO payment_intents (
  order_id, provider, status, amount_minor, currency, provider_reference,
  checkout_url, idempotency_key, capabilities, receipt_metadata
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (provider, idempotency_key) DO UPDATE SET idempotency_key = EXCLUDED.idempotency_key
RETURNING *;

-- name: GetPaymentIntent :one
SELECT * FROM payment_intents WHERE id = $1;

-- name: GetPaymentIntentByIdempotency :one
SELECT * FROM payment_intents WHERE provider = $1 AND idempotency_key = $2;

-- name: AcquirePaymentMutationLock :exec
SELECT pg_advisory_lock(hashtextextended($1, 0));

-- name: ReleasePaymentMutationLock :exec
SELECT pg_advisory_unlock(hashtextextended($1, 0));

-- name: GetPaymentIntentByProviderReference :one
SELECT * FROM payment_intents WHERE provider = $1 AND provider_reference = $2;

-- name: GetPaymentIntentByOrderProvider :one
SELECT * FROM payment_intents WHERE order_id = $1 AND provider = $2 ORDER BY created_at DESC LIMIT 1;

-- name: ListPaymentIntentsForReconciliation :many
-- Intents the provider may have settled without Omniflow hearing about it.
--
-- A `succeeded` intent on an order that is still `pending` is included on
-- purpose: it is a charge that was recorded without being settled, and polling
-- it again routes it through settlement. The order predicate is what keeps a
-- legitimately settled intent out of the batch once its order has moved on.
SELECT pi.* FROM payment_intents pi
WHERE pi.provider IN ('cryptobot','yookassa')
  AND pi.provider_reference IS NOT NULL AND pi.updated_at < now() - interval '1 minute'
  AND (
    pi.status IN ('pending','processing')
    OR (pi.status = 'succeeded' AND EXISTS (
      SELECT 1 FROM orders o WHERE o.id = pi.order_id AND o.state = 'pending'
    ))
  )
ORDER BY pi.updated_at
LIMIT $1;

-- name: UpdatePaymentIntentStatus :one
UPDATE payment_intents
SET status = sqlc.arg(status), provider_reference = COALESCE(sqlc.narg(provider_reference), provider_reference),
    checkout_url = COALESCE(sqlc.narg(checkout_url), checkout_url), updated_at = now()
WHERE id = sqlc.arg(payment_intent_id)
RETURNING *;

-- name: InsertPaymentEvent :one
INSERT INTO payment_events (
  payment_intent_id, type, previous_status, status, amount_minor, currency,
  provider_event_id, details
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (payment_intent_id, provider_event_id, type) DO UPDATE SET details = payment_events.details
RETURNING *;

-- name: InsertWebhookEvent :one
INSERT INTO provider_webhook_events (
  provider, provider_event_id, signature_valid, body_sha256, raw_body, headers
)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (provider, provider_event_id) DO UPDATE SET provider_event_id = EXCLUDED.provider_event_id
RETURNING *;

-- name: CompleteWebhookEvent :one
UPDATE provider_webhook_events
SET status = sqlc.arg(status), error_code = sqlc.narg(error_code), processed_at = now()
WHERE id = sqlc.arg(webhook_event_id)
RETURNING *;

-- name: CreateRefund :one
INSERT INTO refunds (payment_intent_id, status, amount_minor, currency, provider_reference, reason, idempotency_key, receipt_metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (payment_intent_id, idempotency_key) DO UPDATE SET idempotency_key = EXCLUDED.idempotency_key
RETURNING *;

-- name: GetRefundByIdempotency :one
SELECT * FROM refunds WHERE payment_intent_id = $1 AND idempotency_key = $2;

-- name: GetReservedRefundAmount :one
SELECT COALESCE(sum(amount_minor), 0)::bigint AS amount_minor
FROM refunds
WHERE payment_intent_id = $1 AND status IN ('pending', 'processing', 'succeeded');

-- name: ApproveManualPayment :one
INSERT INTO manual_payment_approvals (payment_intent_id, decision, operator_id, reason, idempotency_key, request_id)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (payment_intent_id) DO UPDATE SET payment_intent_id = EXCLUDED.payment_intent_id
RETURNING *;

-- name: CreateLedgerTransaction :one
INSERT INTO ledger_transactions (type, reference_type, reference_id, idempotency_key, reason, actor_id)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (idempotency_key) DO UPDATE SET idempotency_key = EXCLUDED.idempotency_key
RETURNING *;

-- name: GetLedgerTransactionByIdempotency :one
SELECT * FROM ledger_transactions WHERE idempotency_key = $1;

-- name: InsertLedgerEntry :one
INSERT INTO ledger_entries (transaction_id, account_type, user_id, currency, amount_minor, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (transaction_id, account_type, user_id, currency) DO UPDATE SET amount_minor = ledger_entries.amount_minor
RETURNING *;

-- name: ListLedgerEntriesByTransaction :many
SELECT * FROM ledger_entries WHERE transaction_id = $1 ORDER BY account_type, user_id NULLS LAST;

-- name: GetWalletBalance :one
SELECT COALESCE(sum(amount_minor), 0)::bigint AS balance_minor
FROM ledger_entries
WHERE account_type = 'customer_wallet' AND user_id = $1 AND currency = $2
;

-- name: GetAvailableWalletBalance :one
SELECT GREATEST(
  COALESCE((SELECT sum(wallet_entry.amount_minor)
            FROM ledger_entries wallet_entry
            WHERE wallet_entry.account_type = 'customer_wallet' AND wallet_entry.user_id = sqlc.arg(target_user_id) AND wallet_entry.currency = sqlc.arg(target_currency)), 0)
  - COALESCE((SELECT sum(reserved_order.wallet_minor)
              FROM orders reserved_order
              WHERE reserved_order.user_id = sqlc.arg(target_user_id) AND reserved_order.currency = sqlc.arg(target_currency)
                AND reserved_order.state IN ('draft', 'pending')), 0),
  0
)::bigint AS balance_minor;

-- name: ListExpiredWalletCredits :many
SELECT e.*,
  GREATEST(
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
  )::bigint AS amount_to_expire
FROM ledger_entries e
WHERE e.account_type = 'customer_wallet' AND e.amount_minor > 0 AND e.expires_at <= now()
  AND NOT EXISTS (
    SELECT 1 FROM ledger_transactions t
    WHERE t.type = 'expiration' AND t.reference_type = 'ledger_entry' AND t.reference_id = e.id::text
  )
ORDER BY e.created_at, e.id
LIMIT $1;

-- name: CreateEntitlement :one
INSERT INTO entitlements (
  user_id, order_id, plan_version_id, starts_at, ends_at,
  traffic_allowance_bytes, device_limit, remnawave_squad_ids, subscription_id
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (order_id) DO UPDATE SET order_id = EXCLUDED.order_id
RETURNING *;

-- name: GetEntitlement :one
SELECT * FROM entitlements WHERE id = $1;

-- name: ListEntitlementsForReconciliation :many
SELECT * FROM entitlements
WHERE status IN ('active', 'limited', 'disabled', 'expired')
  AND (reconciled_at IS NULL OR reconciled_at < now() - interval '15 minutes')
ORDER BY reconciled_at NULLS FIRST
LIMIT $1;

-- name: GetRemnawaveMappingByCustomer :one
SELECT * FROM remnawave_users WHERE user_id = $1;

-- name: UpsertRemnawaveMapping :one
INSERT INTO remnawave_users (user_id, remnawave_id, observed_state, reconciled_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (user_id) DO UPDATE
SET remnawave_id = EXCLUDED.remnawave_id,
    observed_state = EXCLUDED.observed_state,
    reconciled_at = now()
RETURNING *;

-- name: CreateFulfillmentOperation :one
INSERT INTO fulfillment_operations (
  entitlement_id, operation, idempotency_key, correlation_id, desired_state
)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (idempotency_key) DO UPDATE SET idempotency_key = EXCLUDED.idempotency_key
RETURNING *;

-- name: LockFulfillmentOperation :one
SELECT * FROM fulfillment_operations WHERE id = $1 FOR UPDATE;

-- name: ListStalledFulfillmentOperations :many
-- Operations that should have run by now and have not: a settlement whose
-- process could not insert the job, or a job the queue discarded after its
-- last attempt. The worker re-inserts the job for each; River's uniqueness on
-- the operation ID makes that a no-op when the job is merely queued.
SELECT * FROM fulfillment_operations
WHERE status IN ('pending', 'retrying')
  AND next_attempt_at < sqlc.arg(before)::timestamptz
  AND created_at < sqlc.arg(before)::timestamptz
ORDER BY next_attempt_at
LIMIT sqlc.arg(page_size);

-- name: UpdateFulfillmentOperation :one
UPDATE fulfillment_operations
SET status = sqlc.arg(status), attempt_count = sqlc.arg(attempt_count),
    next_attempt_at = sqlc.arg(next_attempt_at), last_error_code = sqlc.narg(last_error_code),
    updated_at = now(), completed_at = CASE WHEN sqlc.arg(status) IN ('succeeded', 'failed', 'cancelled') THEN now() ELSE NULL END
WHERE id = sqlc.arg(operation_id)
RETURNING *;

-- name: InsertFulfillmentHistory :one
INSERT INTO fulfillment_history (
  operation_id, status, correlation_id, request_summary, response_summary, error_code
)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: UpdateEntitlementObservedState :one
-- The reconciler's write, aware that a paused entitlement is a disabled user.
--
-- `paused_at` is cleared whenever the incoming status is not `paused`, which is
-- what keeps the table's pairing constraint satisfiable. It matters more than it
-- looks: without it, a reconcile that saw a disabled Remnawave user would drop
-- the entitlement out of `paused` and leave the pause instant behind, and the
-- customer would silently lose the days they were owed. With the constraint, the
-- write would simply fail — which is why the clearing belongs here rather than
-- in a caller that could forget.
UPDATE entitlements
SET status = sqlc.arg(status), remnawave_user_id = COALESCE(sqlc.narg(remnawave_user_id), remnawave_user_id),
    observed_state = sqlc.arg(observed_state),
    paused_at = CASE WHEN sqlc.arg(status) = 'paused' THEN paused_at ELSE NULL END,
    reconciled_at = now(), updated_at = now()
WHERE id = sqlc.arg(entitlement_id)
RETURNING *;

-- name: SupersedePreviousEntitlements :exec
-- Superseding is scoped to one subscription so buying a second subscription
-- never retires the first one's entitlement.
UPDATE entitlements
SET status = 'superseded', updated_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND id <> sqlc.arg(current_entitlement_id)
  AND subscription_id IS NOT DISTINCT FROM sqlc.narg(subscription_id)::uuid
  AND status IN ('pending', 'active', 'limited', 'disabled');

-- name: InsertEntitlementDrift :one
INSERT INTO entitlement_drifts (entitlement_id, kind, expected, observed)
VALUES ($1, $2, $3, $4)
ON CONFLICT (entitlement_id, kind) WHERE status = 'open'
DO UPDATE SET expected = EXCLUDED.expected, observed = EXCLUDED.observed, detected_at = now()
RETURNING *;

-- name: ResolveEntitlementDrifts :exec
UPDATE entitlement_drifts
SET status = 'resolved', resolved_at = now()
WHERE entitlement_id = $1 AND status = 'open';

-- name: ListOpenEntitlementDrifts :many
SELECT * FROM entitlement_drifts WHERE status = 'open' ORDER BY detected_at DESC LIMIT $1;

-- name: InsertAuditEvent :one
-- The single writer for the audit trail. `category` and `outcome` are the two
-- axes the admin panel filters and exports on, so every caller classifies its
-- event rather than letting it fall into an untyped bucket.
INSERT INTO audit_events (
  actor_type, actor_id, action, category, outcome,
  target_type, target_id, reason, request_id, metadata
) VALUES (
  sqlc.arg(actor_type), sqlc.narg(actor_id), sqlc.arg(action),
  sqlc.arg(category), sqlc.arg(outcome),
  sqlc.arg(target_type), sqlc.arg(target_id),
  sqlc.narg(reason), sqlc.narg(request_id), sqlc.arg(metadata)
)
RETURNING *;

-- name: PauseEntitlement :one
-- Freezes an entitlement's remaining time.
--
-- The guard is in the predicate rather than in Go: only an active or limited
-- entitlement can be paused, so two operators pressing pause at the same moment
-- produce one pause and one "no rows", instead of a second pause that resets
-- the instant the first one recorded and loses the days between them.
UPDATE entitlements
SET status = 'paused', paused_at = now(), updated_at = now()
WHERE id = sqlc.arg(entitlement_id)
  AND status IN ('active', 'limited')
  AND paused_at IS NULL
RETURNING *;

-- name: ResumeEntitlement :one
-- Gives back exactly the time the pause took.
--
-- `ends_at` moves by the elapsed pause and `paused_seconds` records the same
-- amount, so the two together are a checkable statement: an entitlement whose
-- expiry sits later than its order paid for is explained by its own column.
UPDATE entitlements
SET status = 'active',
    ends_at = ends_at + (now() - paused_at),
    paused_seconds = paused_seconds + GREATEST(0, extract(epoch FROM (now() - paused_at))::bigint),
    paused_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg(entitlement_id)
  AND status = 'paused'
  AND paused_at IS NOT NULL
RETURNING *;
