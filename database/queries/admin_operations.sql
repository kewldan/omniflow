-- Operator panel queries for v0.7: settings, dashboard, customer and finance
-- operations, fulfillment and job diagnostics, and bulk actions.
--
-- Everything here is read or operated by an authenticated operator whose
-- permissions were already checked in `internal/rbac`. The queries carry no
-- authorization of their own precisely so there is only one place that decides
-- what a role may do.

-- ---------------------------------------------------------------------------
-- Commerce settings
-- ---------------------------------------------------------------------------

-- name: GetCommerceSettings :one
SELECT * FROM commerce_settings WHERE singleton;

-- name: SeedCommerceSettings :one
-- Written once, from the environment the installation already had. An
-- installation upgrading from v0.5 therefore keeps the limits its operator
-- configured, and every later change comes from the panel.
INSERT INTO commerce_settings (
  topup_enabled, topup_currency, topup_presets_minor, topup_minimum_minor,
  topup_maximum_minor, topup_window_seconds, topup_window_limit_minor,
  multi_subscription_enabled, max_subscriptions_per_customer
) VALUES (
  sqlc.arg(topup_enabled), sqlc.arg(topup_currency), sqlc.arg(topup_presets_minor),
  sqlc.arg(topup_minimum_minor), sqlc.arg(topup_maximum_minor),
  sqlc.arg(topup_window_seconds), sqlc.arg(topup_window_limit_minor),
  sqlc.arg(multi_subscription_enabled), sqlc.arg(max_subscriptions_per_customer)
)
ON CONFLICT (singleton) DO NOTHING
RETURNING *;

-- name: UpdateTopUpSettings :one
UPDATE commerce_settings
SET topup_enabled = sqlc.arg(topup_enabled),
    topup_currency = sqlc.arg(topup_currency),
    topup_presets_minor = sqlc.arg(topup_presets_minor),
    topup_minimum_minor = sqlc.arg(topup_minimum_minor),
    topup_maximum_minor = sqlc.arg(topup_maximum_minor),
    topup_window_seconds = sqlc.arg(topup_window_seconds),
    topup_window_limit_minor = sqlc.arg(topup_window_limit_minor),
    updated_at = now(),
    updated_by = sqlc.narg(updated_by)
WHERE singleton
RETURNING *;

-- name: UpdateSubscriptionSettings :one
UPDATE commerce_settings
SET multi_subscription_enabled = sqlc.arg(multi_subscription_enabled),
    max_subscriptions_per_customer = sqlc.arg(max_subscriptions_per_customer),
    updated_at = now(),
    updated_by = sqlc.narg(updated_by)
WHERE singleton
RETURNING *;

-- ---------------------------------------------------------------------------
-- Payment provider configuration
-- ---------------------------------------------------------------------------

-- name: ListPaymentProviderSettings :many
SELECT * FROM payment_provider_settings ORDER BY display_order, provider, merchant_id;

-- name: GetPaymentProviderSettings :one
SELECT * FROM payment_provider_settings
WHERE provider = sqlc.arg(provider) AND merchant_id = sqlc.arg(merchant_id);

-- name: UpsertPaymentProviderSettings :one
-- Null ciphertexts mean "keep what is stored". Recurring is written through the
-- separate capability-test statement below, never here, so a settings save can
-- never quietly enable it without a passing test.
INSERT INTO payment_provider_settings (
  provider, merchant_id, enabled, display_order,
  credentials_ciphertext, webhook_secret_ciphertext, updated_by
) VALUES (
  sqlc.arg(provider), sqlc.arg(merchant_id), sqlc.arg(enabled), sqlc.arg(display_order),
  sqlc.narg(credentials_ciphertext), sqlc.narg(webhook_secret_ciphertext), sqlc.narg(updated_by)
)
ON CONFLICT (provider, merchant_id) DO UPDATE
SET enabled = EXCLUDED.enabled,
    display_order = EXCLUDED.display_order,
    credentials_ciphertext = COALESCE(
      EXCLUDED.credentials_ciphertext, payment_provider_settings.credentials_ciphertext
    ),
    webhook_secret_ciphertext = COALESCE(
      EXCLUDED.webhook_secret_ciphertext, payment_provider_settings.webhook_secret_ciphertext
    ),
    updated_by = EXCLUDED.updated_by,
    updated_at = now()
RETURNING *;

-- name: RecordProviderConnectionCheck :one
UPDATE payment_provider_settings
SET connection_status = sqlc.arg(connection_status),
    connection_error_code = sqlc.narg(connection_error_code),
    connection_checked_at = now(),
    updated_at = now()
WHERE provider = sqlc.arg(provider) AND merchant_id = sqlc.arg(merchant_id)
RETURNING *;

-- name: RecordProviderWebhookHealth :one
UPDATE payment_provider_settings
SET webhook_status = sqlc.arg(webhook_status),
    webhook_last_error_code = sqlc.narg(webhook_last_error_code),
    webhook_last_event_at = CASE
      WHEN sqlc.arg(webhook_status)::text = 'healthy' THEN now()
      ELSE webhook_last_event_at
    END,
    updated_at = now()
WHERE provider = sqlc.arg(provider) AND merchant_id = sqlc.arg(merchant_id)
RETURNING *;

-- name: SetProviderRecurring :one
-- The two writes are one statement on purpose: `recurring_enabled` may only be
-- true alongside a passing test, and the table constraint refuses any pair that
-- violates it.
UPDATE payment_provider_settings
SET recurring_test_status = sqlc.arg(recurring_test_status),
    recurring_enabled = sqlc.arg(recurring_enabled),
    recurring_tested_at = now(),
    updated_at = now(),
    updated_by = sqlc.narg(updated_by)
WHERE provider = sqlc.arg(provider) AND merchant_id = sqlc.arg(merchant_id)
RETURNING *;

-- ---------------------------------------------------------------------------
-- Dashboard
-- ---------------------------------------------------------------------------

-- name: DashboardCustomerTotals :one
-- Every count carries its own definition, which the panel repeats beside the
-- number: "active" is a customer record that is neither suspended nor deleted,
-- not one with a live subscription.
SELECT
  count(*) FILTER (WHERE status = 'active')::bigint AS active_customers,
  count(*) FILTER (WHERE status = 'suspended')::bigint AS suspended_customers,
  count(*) FILTER (WHERE status = 'deleted')::bigint AS deleted_customers,
  count(*) FILTER (WHERE created_at >= now() - sqlc.arg(lookback)::interval)::bigint AS new_customers
FROM users;

-- name: DashboardSubscriptionTotals :one
SELECT
  count(*) FILTER (WHERE e.status = 'active' AND e.ends_at > now())::bigint AS active_entitlements,
  count(*) FILTER (WHERE e.status = 'limited')::bigint AS limited_entitlements,
  count(*) FILTER (WHERE e.status IN ('active', 'limited') AND e.ends_at <= now())::bigint AS lapsed_entitlements,
  count(*) FILTER (
    WHERE e.status = 'active' AND e.ends_at BETWEEN now() AND now() + sqlc.arg(lookback)::interval
  )::bigint AS renewals_due,
  COALESCE(sum((s.observed_state->>'usedTrafficBytes')::bigint), 0)::bigint AS observed_traffic_bytes
FROM entitlements e
LEFT JOIN subscriptions s ON s.id = e.subscription_id;

-- name: DashboardPaymentHealth :one
SELECT
  count(*) FILTER (WHERE i.status = 'succeeded')::bigint AS succeeded,
  count(*) FILTER (WHERE i.status IN ('pending', 'requires_action', 'processing'))::bigint AS in_flight,
  count(*) FILTER (WHERE i.status = 'failed')::bigint AS failed,
  count(*) FILTER (
    WHERE i.status IN ('pending', 'requires_action', 'processing')
      AND i.updated_at < now() - sqlc.arg(stuck_after)::interval
  )::bigint AS stuck
FROM payment_intents i
WHERE i.created_at >= now() - sqlc.arg(lookback)::interval;

-- name: DashboardRevenue :many
-- Deliberately three separate figures rather than one "revenue": money taken
-- through a provider, wallet credit spent, and money returned are different
-- questions, and adding them together answers none of them.
SELECT
  o.currency,
  COALESCE(sum(o.paid_minor), 0)::bigint AS paid_minor,
  COALESCE(sum(o.wallet_minor), 0)::bigint AS wallet_minor,
  COALESCE(sum(o.refunded_minor), 0)::bigint AS refunded_minor,
  count(*)::bigint AS order_count
FROM orders o
WHERE o.state IN ('paid', 'fulfilled', 'partially_refunded', 'refunded')
  AND o.updated_at >= now() - sqlc.arg(lookback)::interval
GROUP BY o.currency
ORDER BY o.currency;

-- name: DashboardSupportTotals :one
SELECT
  count(*) FILTER (WHERE status = 'open')::bigint AS open_tickets,
  count(*) FILTER (
    WHERE status = 'open' AND last_message_at < now() - sqlc.arg(stale_after)::interval
  )::bigint AS stale_tickets
FROM support_tickets;

-- name: DashboardJobHealth :one
SELECT
  count(*) FILTER (WHERE status IN ('pending', 'retrying'))::bigint AS queued,
  count(*) FILTER (WHERE status = 'failed')::bigint AS failed,
  count(*) FILTER (
    WHERE status IN ('pending', 'retrying') AND next_attempt_at < now() - sqlc.arg(late_after)::interval
  )::bigint AS overdue
FROM fulfillment_operations;

-- name: DashboardWebhookHealth :one
SELECT
  count(*) FILTER (WHERE status = 'received')::bigint AS unprocessed,
  count(*) FILTER (WHERE status = 'failed')::bigint AS failed,
  count(*) FILTER (WHERE NOT signature_valid)::bigint AS unverified
FROM provider_webhook_events
WHERE received_at >= now() - sqlc.arg(lookback)::interval;

-- name: DashboardDriftTotals :one
SELECT
  count(*) FILTER (WHERE status = 'open')::bigint AS open_drifts,
  count(*) FILTER (WHERE status = 'open' AND kind = 'missing_remote')::bigint AS missing_remote
FROM entitlement_drifts;

-- name: DashboardOutboxLag :one
-- The oldest unpublished event is the honest measure of lag: a queue with a
-- thousand fresh events is healthy, and one with a single event from an hour
-- ago is not.
SELECT
  count(*)::bigint AS unpublished,
  COALESCE(extract(epoch FROM now() - min(occurred_at)), 0)::bigint AS oldest_age_seconds
FROM outbox_events
WHERE published_at IS NULL;

-- name: ListRecentMaintenanceEvents :many
SELECT * FROM maintenance_events ORDER BY occurred_at DESC LIMIT sqlc.arg(page_size);

-- ---------------------------------------------------------------------------
-- Customer search and profile
-- ---------------------------------------------------------------------------

-- name: SearchCustomers :many
-- Search is by identifiers an operator may safely be handed: the Omniflow
-- customer identifier, a Telegram identifier, and a Remnawave username. There
-- is deliberately no free-text search over contact values — those are stored as
-- ciphertext and a fingerprint precisely so they cannot be trawled.
SELECT sqlc.embed(u), r.telegram_id, r.remnawave_id
FROM users u
LEFT JOIN remnawave_users r ON r.user_id = u.id
WHERE (
    sqlc.narg(cursor_created_at)::timestamptz IS NULL
    OR (u.created_at, u.id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
  AND (sqlc.narg(status)::text IS NULL OR u.status = sqlc.narg(status))
  AND (sqlc.narg(customer_id)::uuid IS NULL OR u.id = sqlc.narg(customer_id))
  AND (sqlc.narg(telegram_id)::bigint IS NULL OR r.telegram_id = sqlc.narg(telegram_id))
  AND (
    sqlc.narg(username)::text IS NULL
    OR EXISTS (
      SELECT 1 FROM subscriptions s
      WHERE s.user_id = u.id AND s.remnawave_username = sqlc.narg(username)
    )
  )
  AND (
    sqlc.narg(segment)::text IS NULL
    OR (sqlc.narg(segment) = 'subscribed' AND EXISTS (
          SELECT 1 FROM entitlements e
          WHERE e.user_id = u.id AND e.status IN ('active', 'limited') AND e.ends_at > now()))
    OR (sqlc.narg(segment) = 'lapsed' AND NOT EXISTS (
          SELECT 1 FROM entitlements e
          WHERE e.user_id = u.id AND e.status IN ('active', 'limited') AND e.ends_at > now())
        AND EXISTS (SELECT 1 FROM entitlements e WHERE e.user_id = u.id))
    OR (sqlc.narg(segment) = 'never_purchased' AND NOT EXISTS (
          SELECT 1 FROM orders o WHERE o.user_id = u.id AND o.state IN ('paid', 'fulfilled')))
    OR (sqlc.narg(segment) = 'flagged' AND EXISTS (
          SELECT 1 FROM blocklist_matches m
          WHERE m.user_id = u.id AND m.status IN ('open', 'appealed')))
  )
ORDER BY u.created_at DESC, u.id DESC
LIMIT sqlc.arg(page_size);

-- name: GetCustomerOverview :one
-- One round trip for the profile header: the customer, their Telegram mapping,
-- and the counts the page shows before any tab is opened.
SELECT
  sqlc.embed(u),
  r.telegram_id,
  (SELECT count(*) FROM subscriptions s WHERE s.user_id = u.id AND s.status = 'active')::bigint AS active_subscriptions,
  (SELECT count(*) FROM orders o WHERE o.user_id = u.id)::bigint AS order_count,
  (SELECT count(*) FROM support_tickets t WHERE t.user_id = u.id AND t.status = 'open')::bigint AS open_tickets,
  (SELECT count(*) FROM referral_attributions a WHERE a.referrer_user_id = u.id)::bigint AS referral_count,
  (SELECT count(*) FROM blocklist_matches m WHERE m.user_id = u.id AND m.status IN ('open', 'appealed'))::bigint AS open_flags,
  EXISTS (SELECT 1 FROM blocklist_allowlist a WHERE a.user_id = u.id) AS allowlisted
FROM users u
LEFT JOIN remnawave_users r ON r.user_id = u.id
WHERE u.id = $1;

-- name: ListCustomerSubscriptionsDetailed :many
-- Every concurrent subscription with the entitlement currently governing it, so
-- the panel can offer independent lifecycle actions per subscription rather
-- than assuming one per customer.
--
-- The entitlement columns are selected individually rather than embedded: a
-- subscription that has never been provisioned has no entitlement at all, and
-- an embedded struct would have to scan those nulls into non-nullable fields.
SELECT
  sqlc.embed(s),
  e.id AS entitlement_id,
  COALESCE(e.status, '')::text AS entitlement_status,
  e.starts_at AS entitlement_starts_at,
  e.ends_at AS entitlement_ends_at,
  e.traffic_allowance_bytes,
  e.device_limit,
  e.remnawave_squad_ids,
  p.code AS plan_code,
  v.version AS plan_version
FROM subscriptions s
LEFT JOIN LATERAL (
  SELECT en.* FROM entitlements en
  WHERE en.subscription_id = s.id AND en.status IN ('pending', 'active', 'limited', 'disabled')
  ORDER BY en.ends_at DESC
  LIMIT 1
) e ON true
LEFT JOIN plan_versions v ON v.id = e.plan_version_id
LEFT JOIN plans p ON p.id = v.plan_id
WHERE s.user_id = $1
ORDER BY s.slot;

-- name: ListCustomerOrders :many
SELECT * FROM orders
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT sqlc.arg(page_size);

-- name: ListCustomerLedgerEntries :many
SELECT sqlc.embed(e), t.type, t.reference_type, t.reference_id, t.reason
FROM ledger_entries e
JOIN ledger_transactions t ON t.id = e.transaction_id
WHERE e.account_type = 'customer_wallet' AND e.user_id = $1
ORDER BY e.created_at DESC
LIMIT sqlc.arg(page_size);

-- name: ListCustomerSupportTickets :many
SELECT * FROM support_tickets WHERE user_id = $1 ORDER BY updated_at DESC LIMIT sqlc.arg(page_size);

-- name: ListCustomerConsents :many
SELECT DISTINCT ON (purpose) *
FROM consent_records
WHERE user_id = $1
ORDER BY purpose, occurred_at DESC;

-- ---------------------------------------------------------------------------
-- Finance
-- ---------------------------------------------------------------------------

-- name: SearchOrders :many
SELECT * FROM orders
WHERE (
    sqlc.narg(cursor_created_at)::timestamptz IS NULL
    OR (created_at, id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
  AND (sqlc.narg(state)::text IS NULL OR state = sqlc.narg(state))
  AND (sqlc.narg(operation)::text IS NULL OR operation = sqlc.narg(operation))
  AND (sqlc.narg(user_id)::uuid IS NULL OR user_id = sqlc.narg(user_id))
  AND (sqlc.narg(currency)::text IS NULL OR currency = sqlc.narg(currency))
  AND (sqlc.narg(created_from)::timestamptz IS NULL OR created_at >= sqlc.narg(created_from))
  AND (sqlc.narg(created_to)::timestamptz IS NULL OR created_at < sqlc.narg(created_to))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_size);

-- name: ListOrderPaymentIntents :many
SELECT * FROM payment_intents WHERE order_id = $1 ORDER BY created_at;

-- name: ListPaymentEventsForIntent :many
SELECT * FROM payment_events WHERE payment_intent_id = $1 ORDER BY occurred_at;

-- name: ListRefundsForOrder :many
SELECT r.* FROM refunds r
JOIN payment_intents i ON i.id = r.payment_intent_id
WHERE i.order_id = $1
ORDER BY r.created_at;

-- name: ListStuckPaymentIntents :many
-- Intents that have been in flight longer than a provider should take. The
-- panel offers reconciliation and retry from this list; neither mutates money
-- directly, both go through the existing idempotent payment service.
SELECT sqlc.embed(i), o.user_id, o.operation
FROM payment_intents i
JOIN orders o ON o.id = i.order_id
WHERE i.status IN ('pending', 'requires_action', 'processing')
  AND i.updated_at < now() - sqlc.arg(stuck_after)::interval
ORDER BY i.updated_at
LIMIT sqlc.arg(page_size);

-- name: ExportFinanceRows :many
-- The CSV export's source. Column order here is the column order in the file,
-- and the schema is stable: a new column is appended, never inserted, so a
-- spreadsheet or importer built against an older export keeps working.
SELECT
  o.id AS order_id,
  o.created_at,
  o.updated_at,
  o.user_id,
  o.state,
  o.operation,
  o.currency,
  o.subtotal_minor,
  o.discount_minor,
  o.wallet_minor,
  o.external_minor,
  o.paid_minor,
  o.refunded_minor,
  (
    SELECT string_agg(DISTINCT i.provider, '|' ORDER BY i.provider)
    FROM payment_intents i WHERE i.order_id = o.id
  ) AS providers
FROM orders o
WHERE (sqlc.narg(created_from)::timestamptz IS NULL OR o.created_at >= sqlc.narg(created_from))
  AND (sqlc.narg(created_to)::timestamptz IS NULL OR o.created_at < sqlc.narg(created_to))
  AND (sqlc.narg(state)::text IS NULL OR o.state = sqlc.narg(state))
  AND (
    sqlc.narg(cursor_created_at)::timestamptz IS NULL
    OR (o.created_at, o.id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
ORDER BY o.created_at DESC, o.id DESC
LIMIT sqlc.arg(page_size);

-- ---------------------------------------------------------------------------
-- Fulfillment and jobs
-- ---------------------------------------------------------------------------

-- name: SearchFulfillmentOperations :many
SELECT sqlc.embed(f), e.user_id, e.subscription_id
FROM fulfillment_operations f
JOIN entitlements e ON e.id = f.entitlement_id
WHERE (
    sqlc.narg(cursor_created_at)::timestamptz IS NULL
    OR (f.created_at, f.id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
  AND (sqlc.narg(status)::text IS NULL OR f.status = sqlc.narg(status))
  AND (sqlc.narg(operation)::text IS NULL OR f.operation = sqlc.narg(operation))
  AND (sqlc.narg(entitlement_id)::uuid IS NULL OR f.entitlement_id = sqlc.narg(entitlement_id))
ORDER BY f.created_at DESC, f.id DESC
LIMIT sqlc.arg(page_size);

-- name: GetFulfillmentOperation :one
SELECT * FROM fulfillment_operations WHERE id = $1;

-- name: ListFulfillmentHistoryForOperation :many
SELECT * FROM fulfillment_history WHERE operation_id = $1 ORDER BY occurred_at;

-- name: RequeueFulfillmentOperation :one
-- An operator retry only ever moves a terminal-looking row back into the queue.
-- It cannot skip the idempotency key, so the retried attempt is the same
-- operation the worker already knows how to make exactly once.
UPDATE fulfillment_operations
SET status = 'pending', next_attempt_at = now(), last_error_code = NULL, updated_at = now()
WHERE id = sqlc.arg(operation_id) AND status IN ('failed', 'retrying')
RETURNING *;

-- name: CancelFulfillmentOperation :one
-- Only an operation that has not yet succeeded may be cancelled, so a completed
-- provisioning can never be retracted by a panel click.
UPDATE fulfillment_operations
SET status = 'cancelled', completed_at = now(), updated_at = now()
WHERE id = sqlc.arg(operation_id) AND status IN ('pending', 'retrying', 'failed')
RETURNING *;

-- name: SearchWebhookEvents :many
-- The raw body is deliberately not selected: it can contain provider payloads,
-- and the panel never needs it to answer "did this arrive and was it accepted".
SELECT id, provider, provider_event_id, signature_valid, status, error_code,
       received_at, processed_at, retain_until
FROM provider_webhook_events
WHERE (
    sqlc.narg(cursor_received_at)::timestamptz IS NULL
    OR (received_at, id) < (sqlc.narg(cursor_received_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
  AND (sqlc.narg(provider)::text IS NULL OR provider = sqlc.narg(provider))
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
ORDER BY received_at DESC, id DESC
LIMIT sqlc.arg(page_size);

-- name: GetWebhookEventForReplay :one
SELECT * FROM provider_webhook_events WHERE id = $1;

-- name: MarkWebhookEventForReplay :one
-- Reprocessing is replay-safe because the downstream handlers are keyed on the
-- provider event identifier, so a second pass over the same body reaches the
-- same terminal state instead of applying twice.
UPDATE provider_webhook_events
SET status = 'received', error_code = NULL, processed_at = NULL
WHERE id = sqlc.arg(event_id) AND status IN ('failed', 'ignored')
RETURNING *;

-- name: ListUnpublishedOutboxEvents :many
SELECT id, topic, occurred_at FROM outbox_events
WHERE published_at IS NULL
ORDER BY occurred_at
LIMIT sqlc.arg(page_size);

-- name: ListOpenDriftsDetailed :many
SELECT sqlc.embed(d), e.user_id, e.subscription_id, e.remnawave_user_id
FROM entitlement_drifts d
JOIN entitlements e ON e.id = d.entitlement_id
WHERE d.status = 'open'
ORDER BY d.detected_at DESC
LIMIT sqlc.arg(page_size);

-- ---------------------------------------------------------------------------
-- Bulk operator actions
-- ---------------------------------------------------------------------------

-- name: CreateBulkOperation :one
INSERT INTO bulk_operations (kind, requested_by, reason, parameters, idempotency_key)
VALUES (
  sqlc.arg(kind), sqlc.arg(requested_by), sqlc.arg(reason),
  sqlc.arg(parameters), sqlc.arg(idempotency_key)
)
ON CONFLICT (idempotency_key) DO NOTHING
RETURNING *;

-- name: GetBulkOperation :one
SELECT * FROM bulk_operations WHERE id = $1;

-- name: GetBulkOperationByIdempotency :one
SELECT * FROM bulk_operations WHERE idempotency_key = $1;

-- name: ListBulkOperations :many
SELECT * FROM bulk_operations ORDER BY created_at DESC LIMIT sqlc.arg(page_size);

-- name: InsertBulkOperationItem :exec
INSERT INTO bulk_operation_items (operation_id, position, target_type, target_id)
VALUES (sqlc.arg(operation_id), sqlc.arg(position), sqlc.arg(target_type), sqlc.arg(target_id))
ON CONFLICT (operation_id, position) DO NOTHING;

-- name: SetBulkOperationTotal :one
UPDATE bulk_operations
SET total_count = sqlc.arg(total_count), status = 'ready', updated_at = now()
WHERE id = sqlc.arg(operation_id) AND status = 'previewing'
RETURNING *;

-- name: StartBulkOperation :one
-- Only a previewed operation may run. That is what enforces "impact preview
-- before bulk change" in the database rather than only in the panel.
UPDATE bulk_operations
SET status = 'running', updated_at = now()
WHERE id = sqlc.arg(operation_id) AND status = 'ready'
RETURNING *;

-- name: ListPendingBulkOperationItems :many
SELECT * FROM bulk_operation_items
WHERE operation_id = sqlc.arg(operation_id) AND status = 'pending'
ORDER BY position
LIMIT sqlc.arg(page_size);

-- name: ListBulkOperationItems :many
SELECT * FROM bulk_operation_items
WHERE operation_id = $1
ORDER BY position
LIMIT sqlc.arg(page_size);

-- name: CompleteBulkOperationItem :one
UPDATE bulk_operation_items
SET status = sqlc.arg(status), error_code = sqlc.narg(error_code), processed_at = now()
WHERE operation_id = sqlc.arg(operation_id)
  AND position = sqlc.arg(position)
  AND status = 'pending'
RETURNING *;

-- name: RecountBulkOperation :one
-- Counters are recomputed from the items rather than incremented, so a retried
-- worker cannot double-count an outcome it already recorded.
UPDATE bulk_operations b
SET succeeded_count = c.succeeded,
    failed_count = c.failed,
    skipped_count = c.skipped,
    status = CASE
      WHEN c.pending > 0 THEN b.status
      WHEN c.failed = c.total AND c.total > 0 THEN 'failed'
      ELSE 'completed'
    END,
    completed_at = CASE WHEN c.pending > 0 THEN NULL ELSE now() END,
    updated_at = now()
FROM (
  SELECT
    count(*)::integer AS total,
    count(*) FILTER (WHERE status = 'pending')::integer AS pending,
    count(*) FILTER (WHERE status = 'succeeded')::integer AS succeeded,
    count(*) FILTER (WHERE status = 'failed')::integer AS failed,
    count(*) FILTER (WHERE status = 'skipped')::integer AS skipped
  FROM bulk_operation_items WHERE operation_id = sqlc.arg(operation_id)
) c
WHERE b.id = sqlc.arg(operation_id)
RETURNING b.*;

-- name: CancelBulkOperation :one
UPDATE bulk_operations
SET status = 'cancelled', completed_at = now(), updated_at = now()
WHERE id = sqlc.arg(operation_id) AND status IN ('previewing', 'ready')
RETURNING *;

-- ---------------------------------------------------------------------------
-- Catalogue administration
-- ---------------------------------------------------------------------------

-- name: ListPlansAdmin :many
-- The catalogue as an operator sees it: archived plans included, with the
-- current version and how many orders reference the plan, so archiving
-- something in active use is a visible decision rather than a surprise.
SELECT
  sqlc.embed(p),
  (SELECT max(version) FROM plan_versions v WHERE v.plan_id = p.id)::integer AS latest_version,
  (SELECT count(*) FROM order_lines l WHERE l.plan_id = p.id)::bigint AS order_line_count
FROM plans p
ORDER BY p.sort_order, p.code;

-- name: GetPlanAdmin :one
SELECT * FROM plans WHERE id = $1;

-- name: ListPlanVersions :many
SELECT * FROM plan_versions WHERE plan_id = $1 ORDER BY version DESC;

-- name: ListPlanPrices :many
SELECT * FROM plan_prices WHERE plan_version_id = $1 ORDER BY currency;

-- name: ListPlanLocalizations :many
SELECT * FROM plan_localizations WHERE plan_id = $1 ORDER BY locale;

-- name: UpdatePlanOrdering :one
UPDATE plans
SET sort_order = sqlc.arg(sort_order),
    max_concurrent_per_customer = sqlc.narg(max_concurrent_per_customer)
WHERE id = sqlc.arg(plan_id)
RETURNING *;

-- name: ArchivePlan :one
UPDATE plans
SET archived_at = CASE WHEN sqlc.arg(archived)::boolean THEN COALESCE(archived_at, now()) ELSE NULL END,
    visible = CASE WHEN sqlc.arg(archived)::boolean THEN false ELSE visible END
WHERE id = sqlc.arg(plan_id)
RETURNING *;

-- name: RetirePlanVersion :one
UPDATE plan_versions
SET retired_at = now()
WHERE id = sqlc.arg(plan_version_id) AND retired_at IS NULL
RETURNING *;

-- name: SearchPromotions :many
SELECT
  sqlc.embed(p),
  (SELECT count(*) FROM promo_redemptions r WHERE r.promotion_id = p.id)::bigint AS redemption_count,
  (SELECT COALESCE(sum(r.discount_minor), 0) FROM promo_redemptions r WHERE r.promotion_id = p.id)::bigint AS discount_minor
FROM promotions p
WHERE (sqlc.narg(active)::boolean IS NULL OR p.active = sqlc.narg(active))
  AND (sqlc.narg(kind)::text IS NULL OR p.kind = sqlc.narg(kind))
ORDER BY p.created_at DESC
LIMIT sqlc.arg(page_size);

-- name: GetPromotionAdmin :one
SELECT * FROM promotions WHERE id = $1;

-- name: UpdatePromotion :one
UPDATE promotions
SET value = sqlc.arg(value),
    currency = sqlc.narg(currency),
    starts_at = sqlc.narg(starts_at),
    ends_at = sqlc.narg(ends_at),
    redemption_limit = sqlc.narg(redemption_limit),
    per_customer_limit = sqlc.arg(per_customer_limit),
    eligibility = sqlc.arg(eligibility),
    active = sqlc.arg(active),
    stackable = sqlc.arg(stackable),
    precedence = sqlc.arg(precedence)
WHERE id = sqlc.arg(promotion_id)
RETURNING *;

-- name: ListPromoCodesForPromotion :many
SELECT
  sqlc.embed(c),
  (SELECT count(*) FROM promo_redemptions r WHERE r.promo_code_id = c.id)::bigint AS redemption_count
FROM promo_codes c
WHERE c.promotion_id = $1
ORDER BY c.created_at DESC;

-- name: SetPromoCodeActive :one
UPDATE promo_codes SET active = sqlc.arg(active) WHERE id = sqlc.arg(promo_code_id) RETURNING *;

-- name: ListPromotionPlans :many
SELECT p.* FROM promotion_plans j JOIN plans p ON p.id = j.plan_id
WHERE j.promotion_id = $1
ORDER BY p.code;

-- name: RemovePromotionPlan :exec
DELETE FROM promotion_plans WHERE promotion_id = sqlc.arg(promotion_id) AND plan_id = sqlc.arg(plan_id);

-- name: ListAddonsAdmin :many
SELECT
  sqlc.embed(a),
  (SELECT max(version) FROM addon_versions v WHERE v.addon_id = a.id)::integer AS latest_version
FROM addons a
ORDER BY a.sort_order, a.code;

-- name: ListAddonVersions :many
SELECT * FROM addon_versions WHERE addon_id = $1 ORDER BY version DESC;

-- name: ListAddonPrices :many
SELECT * FROM addon_prices WHERE addon_version_id = $1 ORDER BY currency;
