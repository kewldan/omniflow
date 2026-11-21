-- The Bedolaga importer (cmd/migrate-bedolaga).
--
-- Everything the importer writes lands in ordinary Omniflow tables; these
-- statements are the few shapes no other path needed: a customer created with
-- its original sign-up date, a subscription that adopts a Remnawave user that
-- already exists, and an entitlement that is already active because the panel
-- already serves it. Nothing here pushes to Remnawave: no fulfillment
-- operation is created, so no job exists to run one.

-- ---------------------------------------------------------------------------
-- Runs and items
-- ---------------------------------------------------------------------------

-- name: CreateBedolagaImportRun :one
INSERT INTO bedolaga_import_runs (actor) VALUES (sqlc.arg(actor)) RETURNING *;

-- name: FinishBedolagaImportRun :one
UPDATE bedolaga_import_runs
SET status = sqlc.arg(status), report = sqlc.arg(report), finished_at = now()
WHERE id = sqlc.arg(run_id) AND status = 'running'
RETURNING *;

-- name: ListBedolagaImportRuns :many
SELECT * FROM bedolaga_import_runs ORDER BY started_at DESC LIMIT sqlc.arg(page_size);

-- name: ListBedolagaImportItems :many
SELECT * FROM bedolaga_import_items ORDER BY kind, source_id;

-- name: LockBedolagaImportItem :one
SELECT * FROM bedolaga_import_items
WHERE kind = sqlc.arg(kind) AND source_id = sqlc.arg(source_id)
FOR UPDATE;

-- name: UpsertBedolagaImportItem :one
-- Records a decision. An applied row is never moved back to a conflict or a
-- skip: what the importer created exists whatever the source says later, and
-- the row is the only link from that Omniflow record back to Bedolaga.
INSERT INTO bedolaga_import_items (
  kind, source_id, status, codes, user_id, subscription_id, entitlement_id,
  fingerprint, last_run_id, applied_at
)
VALUES (
  sqlc.arg(kind), sqlc.arg(source_id), sqlc.arg(status), sqlc.arg(codes),
  sqlc.narg(user_id), sqlc.narg(subscription_id), sqlc.narg(entitlement_id),
  sqlc.arg(fingerprint), sqlc.arg(run_id),
  CASE WHEN sqlc.arg(status)::text = 'applied' THEN now() ELSE NULL END
)
ON CONFLICT (kind, source_id) DO UPDATE
SET status = EXCLUDED.status,
    codes = EXCLUDED.codes,
    user_id = COALESCE(EXCLUDED.user_id, bedolaga_import_items.user_id),
    subscription_id = COALESCE(EXCLUDED.subscription_id, bedolaga_import_items.subscription_id),
    entitlement_id = COALESCE(EXCLUDED.entitlement_id, bedolaga_import_items.entitlement_id),
    fingerprint = EXCLUDED.fingerprint,
    last_run_id = EXCLUDED.last_run_id,
    applied_at = COALESCE(bedolaga_import_items.applied_at, EXCLUDED.applied_at),
    updated_at = now()
WHERE bedolaga_import_items.status <> 'applied' OR EXCLUDED.status = 'applied'
RETURNING *;

-- ---------------------------------------------------------------------------
-- What the target already holds
-- ---------------------------------------------------------------------------

-- name: ListTelegramOwners :many
-- Every Telegram ID an Omniflow customer already holds, through an active
-- identity or through the Remnawave mapping the bot adopts by. Either one
-- means a second customer for that ID could never be resolved.
SELECT provider_subject AS telegram_subject, user_id
FROM identities
WHERE provider = 'telegram' AND status = 'active'
UNION
SELECT telegram_id::text, user_id
FROM remnawave_users
WHERE telegram_id IS NOT NULL;

-- name: ListLinkedRemnawaveUsers :many
-- Every Remnawave user an Omniflow customer already owns.
SELECT remnawave_user_id::bigint AS remnawave_id, user_id
FROM subscriptions
WHERE remnawave_user_id IS NOT NULL
UNION
SELECT remnawave_id, user_id
FROM remnawave_users;

-- name: ListPlanVersionsForImport :many
SELECT v.id, v.plan_id, p.code AS plan_code, p.kind AS plan_kind, v.version,
       v.billing_period, v.duration_seconds, v.traffic_allowance_bytes,
       v.device_limit, v.remnawave_squad_ids, v.upgrade_policy,
       v.downgrade_policy, v.cancellation_policy, v.grace_period_seconds,
       v.retired_at
FROM plan_versions v
JOIN plans p ON p.id = v.plan_id
ORDER BY p.code, v.version;

-- name: ListReferralAttributionPairs :many
SELECT referred_user_id, referrer_user_id FROM referral_attributions;

-- name: SumBedolagaWalletMovements :many
-- What the importer has moved into each wallet so far, keyed by Bedolaga user.
-- The ledger is the record rather than a counter beside it: a run that dies
-- between the movement and its bookkeeping still sees the movement here, so
-- the next run computes a zero difference instead of crediting twice.
SELECT split_part(t.reference_id, ':', 2)::bigint AS source_user_id,
       e.user_id, e.currency,
       sum(e.amount_minor)::bigint AS total_minor,
       count(DISTINCT t.id)::integer AS movements
FROM ledger_transactions t
JOIN ledger_entries e ON e.transaction_id = t.id AND e.account_type = 'customer_wallet'
WHERE t.reference_type = 'operator_adjustment'
  AND t.reference_id LIKE 'bedolaga-user:%'
GROUP BY 1, 2, 3;

-- name: ListBedolagaLegacyPayments :many
-- The source rows already carried over, so a repeated run plans only the
-- payments taken since.
SELECT source, source_id FROM legacy_payments WHERE source IN ('bedolaga', 'bedolaga_gift');

-- name: ListBedolagaReferralAliases :many
SELECT code, user_id FROM referral_code_aliases;

-- name: ListLegacyDiscounts :many
SELECT user_id, last_discount_at FROM legacy_customer_discounts WHERE source = 'bedolaga';

-- name: ListBedolagaLegacyPeriods :many
SELECT source_id, user_id, is_trial, starts_at, ends_at
FROM legacy_subscription_periods WHERE source = 'bedolaga';

-- name: ListTrialClaimHolders :many
SELECT user_id FROM trial_claims;

-- ---------------------------------------------------------------------------
-- Writes
-- ---------------------------------------------------------------------------

-- name: InsertImportedCustomer :one
-- A customer carried over keeps the date they first signed up, which loyalty
-- tenure, cohorts, and support all read.
INSERT INTO users (status, locale, created_at, updated_at, suspended_at)
VALUES (
  sqlc.arg(status), sqlc.arg(locale), sqlc.arg(created_at), now(),
  CASE WHEN sqlc.arg(status)::text = 'suspended' THEN now() ELSE NULL END
)
RETURNING *;

-- name: InsertImportedRemnawaveMapping :exec
INSERT INTO remnawave_users (user_id, remnawave_id, telegram_id, observed_state, reconciled_at)
VALUES (sqlc.arg(user_id), sqlc.arg(remnawave_id), sqlc.narg(telegram_id), sqlc.arg(observed_state), now());

-- name: InsertImportedSubscription :one
-- Takes the customer's next free slot and adopts the Remnawave user that
-- already serves this subscription, so the access link the customer has
-- keeps working and a later renewal patches that user rather than minting a
-- new one.
INSERT INTO subscriptions (
  user_id, slot, label, remnawave_user_id, remnawave_username, observed_state,
  reconciled_at, created_at
)
SELECT sqlc.arg(user_id), COALESCE(max(slot), 0) + 1, sqlc.arg(label),
       sqlc.arg(remnawave_user_id), sqlc.narg(remnawave_username),
       sqlc.arg(observed_state), now(), sqlc.arg(created_at)
FROM subscriptions WHERE user_id = sqlc.arg(user_id)
RETURNING *;

-- name: InsertImportedOrder :one
-- The zero-value order an imported entitlement hangs off. It is fulfilled at
-- birth because the period was paid for in Bedolaga; carrying no amount, it
-- adds nothing to revenue. The idempotency key starts with `bedolaga:` so the
-- order can always be told apart from a sale.
INSERT INTO orders (
  user_id, state, operation, currency, subtotal_minor, discount_minor,
  wallet_minor, external_minor, paid_minor, idempotency_key, subscription_id,
  created_at, updated_at, paid_at
)
VALUES (
  sqlc.arg(user_id), 'fulfilled', 'purchase', sqlc.arg(currency), 0, 0, 0, 0, 0,
  sqlc.arg(idempotency_key), sqlc.arg(subscription_id), sqlc.arg(created_at), now(),
  sqlc.arg(created_at)
)
ON CONFLICT (user_id, idempotency_key) DO UPDATE SET idempotency_key = EXCLUDED.idempotency_key
RETURNING *;

-- name: InsertImportedEntitlement :one
INSERT INTO entitlements (
  user_id, order_id, plan_version_id, status, starts_at, ends_at,
  traffic_allowance_bytes, device_limit, remnawave_squad_ids,
  remnawave_user_id, subscription_id, observed_state
)
VALUES (
  sqlc.arg(user_id), sqlc.arg(order_id), sqlc.arg(plan_version_id), sqlc.arg(status),
  sqlc.arg(starts_at), sqlc.arg(ends_at), sqlc.narg(traffic_allowance_bytes),
  sqlc.narg(device_limit), sqlc.arg(remnawave_squad_ids), sqlc.arg(remnawave_user_id),
  sqlc.arg(subscription_id), sqlc.arg(observed_state)
)
ON CONFLICT (order_id) DO UPDATE SET order_id = EXCLUDED.order_id
RETURNING *;

-- name: RefreshImportedEntitlement :one
-- Brings an imported entitlement up to date with the source during the
-- delta run. It only touches an entitlement Omniflow has never acted on: once
-- any fulfillment operation exists for it, Omniflow owns its state and the
-- source no longer does, so zero rows come back and the caller reports it.
UPDATE entitlements
SET status = sqlc.arg(status), ends_at = sqlc.arg(ends_at),
    traffic_allowance_bytes = sqlc.narg(traffic_allowance_bytes),
    device_limit = sqlc.narg(device_limit),
    remnawave_squad_ids = sqlc.arg(remnawave_squad_ids),
    observed_state = sqlc.arg(observed_state), updated_at = now()
WHERE entitlements.id = sqlc.arg(entitlement_id)
  AND NOT EXISTS (SELECT 1 FROM fulfillment_operations f WHERE f.entitlement_id = sqlc.arg(entitlement_id))
RETURNING *;

-- name: InsertImportedReferral :one
-- The pair as Bedolaga recorded it. A referee who already paid in Bedolaga
-- arrives qualified: their inviter was rewarded there, and an attribution
-- that is already qualified never pays again.
INSERT INTO referral_attributions (referred_user_id, referrer_user_id, code, created_at, qualified_at)
VALUES (
  sqlc.arg(referred_user_id), sqlc.arg(referrer_user_id), sqlc.arg(code),
  sqlc.arg(created_at), sqlc.narg(qualified_at)
)
ON CONFLICT (referred_user_id) DO NOTHING
RETURNING *;

-- name: InsertImportedTrialClaim :execrows
-- The trial a customer already had, or could no longer have had, in the
-- previous bot. It is dated when that happened rather than now, so trial
-- reports do not show the whole carried-over base as claimed on migration day.
-- A claim the customer already holds is left alone.
INSERT INTO trial_claims (user_id, plan_id, order_id, origin, claimed_at)
VALUES (sqlc.arg(user_id), sqlc.arg(plan_id), sqlc.narg(order_id), sqlc.arg(origin), sqlc.arg(claimed_at))
ON CONFLICT (user_id) DO NOTHING;

-- name: InsertLegacyPayment :execrows
-- A payment the previous bot took. Inserted once per source row; a repeated
-- run inserts nothing.
INSERT INTO legacy_payments (user_id, source, source_id, method, amount_minor, currency, paid_at)
VALUES (sqlc.arg(user_id), sqlc.arg(source), sqlc.arg(source_id), sqlc.arg(method),
        sqlc.arg(amount_minor), sqlc.arg(currency), sqlc.arg(paid_at))
ON CONFLICT (source, source_id) DO NOTHING;

-- name: InsertReferralAlias :execrows
-- A Bedolaga referral code, upper-cased, beside the customer's Omniflow code.
-- A code somebody else already holds is left with them.
INSERT INTO referral_code_aliases (code, user_id, source)
VALUES (sqlc.arg(code), sqlc.arg(user_id), 'bedolaga')
ON CONFLICT (code) DO NOTHING;

-- name: UpsertLegacyDiscount :execrows
-- The customer's last discount in the previous bot. It only ever moves later.
INSERT INTO legacy_customer_discounts (user_id, source, last_discount_at)
VALUES (sqlc.arg(user_id), 'bedolaga', sqlc.arg(last_discount_at))
ON CONFLICT (user_id) DO UPDATE
SET last_discount_at = EXCLUDED.last_discount_at, updated_at = now()
WHERE legacy_customer_discounts.last_discount_at < EXCLUDED.last_discount_at;

-- name: UpsertLegacyPeriod :execrows
-- A subscription period of the previous bot. A repeated run before cutover
-- moves its dates when the previous bot renewed or cut it short.
INSERT INTO legacy_subscription_periods (user_id, source, source_id, is_trial, starts_at, ends_at)
VALUES (sqlc.arg(user_id), 'bedolaga', sqlc.arg(source_id), sqlc.arg(is_trial),
        sqlc.arg(starts_at), sqlc.arg(ends_at))
ON CONFLICT (source, source_id) DO UPDATE
SET is_trial = EXCLUDED.is_trial, starts_at = EXCLUDED.starts_at,
    ends_at = EXCLUDED.ends_at, updated_at = now()
WHERE (legacy_subscription_periods.is_trial, legacy_subscription_periods.starts_at, legacy_subscription_periods.ends_at)
  IS DISTINCT FROM (EXCLUDED.is_trial, EXCLUDED.starts_at, EXCLUDED.ends_at);

-- ---------------------------------------------------------------------------
-- Verification
-- ---------------------------------------------------------------------------

-- name: ListImportedCustomersForVerify :many
SELECT i.source_id, i.user_id, u.status,
       COALESCE((SELECT ident.provider_subject FROM identities ident
         WHERE ident.user_id = i.user_id AND ident.provider = 'telegram' AND ident.status = 'active'
         LIMIT 1), '')::text AS telegram_subject
FROM bedolaga_import_items i
JOIN users u ON u.id = i.user_id
WHERE i.kind = 'user' AND i.status = 'applied';

-- name: ListImportedSubscriptionsForVerify :many
SELECT i.source_id, i.user_id, s.remnawave_user_id, e.id AS entitlement_id,
       e.status AS entitlement_status, e.ends_at
FROM bedolaga_import_items i
JOIN subscriptions s ON s.id = i.subscription_id
LEFT JOIN entitlements e ON e.id = i.entitlement_id
WHERE i.kind = 'subscription' AND i.status = 'applied';

-- name: ListImportedReferralsForVerify :many
SELECT i.source_id, i.user_id, a.referrer_user_id
FROM bedolaga_import_items i
LEFT JOIN referral_attributions a ON a.referred_user_id = i.user_id
WHERE i.kind = 'referral' AND i.status = 'applied';
