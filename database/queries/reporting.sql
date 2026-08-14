-- Sales reporting over a period an operator chooses.
--
-- Every query here keys on `orders.paid_at` rather than `updated_at`, so the
-- same closed period reports the same figures however many refunds are recorded
-- afterwards. Refunds are reported on their own date for the same reason: "what
-- did we refund in March" and "what did we sell in February that was later
-- refunded" are different questions, and answering the first with the second is
-- how a refund appears to reduce a month that had already closed.
--
-- Provider money and wallet credit stay separate in every one of these, as they
-- do on the dashboard. Wallet credit was already counted as revenue when the
-- balance was funded, so adding the two counts it twice.

-- name: SalesByOperation :many
-- What was sold, by what kind of sale. This is the breakdown that separates a
-- new purchase from a renewal, an add-on, and a wallet top-up.
SELECT
  o.operation,
  o.currency,
  count(*)::bigint AS order_count,
  COALESCE(sum(o.subtotal_minor), 0)::bigint AS subtotal_minor,
  COALESCE(sum(o.discount_minor), 0)::bigint AS discount_minor,
  COALESCE(sum(o.paid_minor), 0)::bigint AS paid_minor,
  COALESCE(sum(o.wallet_minor), 0)::bigint AS wallet_minor
FROM orders o
WHERE o.paid_at >= sqlc.arg(since) AND o.paid_at < sqlc.arg(until)
  AND (sqlc.narg(currency)::text IS NULL OR o.currency = sqlc.narg(currency)::text)
GROUP BY o.operation, o.currency
ORDER BY o.currency, o.operation;

-- name: SalesByPlan :many
-- What was sold, by plan and billing period.
--
-- It reads the plan code and the period off the version the order actually
-- referenced rather than off the plan's current state, which is the whole
-- reason plan versions exist: a price change last week must not re-price a sale
-- from last month.
SELECT
  p.code AS plan_code,
  pv.version AS plan_version,
  pv.billing_period,
  o.currency,
  count(DISTINCT o.id)::bigint AS order_count,
  COALESCE(sum(ol.unit_amount_minor * ol.quantity), 0)::bigint AS gross_minor
FROM orders o
JOIN order_lines ol ON ol.order_id = o.id
JOIN plan_versions pv ON pv.id = ol.plan_version_id
JOIN plans p ON p.id = pv.plan_id
WHERE o.paid_at >= sqlc.arg(since) AND o.paid_at < sqlc.arg(until)
  AND (sqlc.narg(currency)::text IS NULL OR o.currency = sqlc.narg(currency)::text)
GROUP BY p.code, pv.version, pv.billing_period, o.currency
ORDER BY gross_minor DESC, p.code, pv.version;

-- name: SalesByDay :many
-- One row per day with a sale in it.
--
-- The day boundary is computed in the installation's own timezone, passed in by
-- the caller, because "sales on the 3rd" means the operator's 3rd and a report
-- bucketed in UTC puts an evening sale in Vladivostok on the wrong day.
SELECT
  (date_trunc('day', o.paid_at AT TIME ZONE sqlc.arg(timezone)::text))::date AS day,
  o.currency,
  count(*)::bigint AS order_count,
  COALESCE(sum(o.paid_minor), 0)::bigint AS paid_minor,
  COALESCE(sum(o.wallet_minor), 0)::bigint AS wallet_minor
FROM orders o
WHERE o.paid_at >= sqlc.arg(since) AND o.paid_at < sqlc.arg(until)
  AND (sqlc.narg(currency)::text IS NULL OR o.currency = sqlc.narg(currency)::text)
GROUP BY 1, o.currency
ORDER BY 1, o.currency;

-- name: RefundsInPeriod :many
-- Refunds by the date they were issued, which is the only date on which an
-- operator can act.
SELECT
  r.currency,
  count(*)::bigint AS refund_count,
  COALESCE(sum(r.amount_minor), 0)::bigint AS refunded_minor
FROM refunds r
WHERE r.created_at >= sqlc.arg(since) AND r.created_at < sqlc.arg(until)
  AND r.status = 'succeeded'
  AND (sqlc.narg(currency)::text IS NULL OR r.currency = sqlc.narg(currency)::text)
GROUP BY r.currency
ORDER BY r.currency;

-- name: TrialConversion :one
-- Of the trials claimed in this period, how many of those customers have since
-- paid for something.
--
-- It is a cohort measure and reads low for a window that ends today, because a
-- trial claimed yesterday has had a day to convert. That is a property of the
-- question rather than a fault in the query, and the panel says so beside the
-- figure rather than quietly excluding recent trials.
WITH claims AS (
  SELECT tc.user_id, tc.claimed_at
  FROM trial_claims tc
  WHERE tc.claimed_at >= sqlc.arg(since) AND tc.claimed_at < sqlc.arg(until)
)
SELECT
  count(*)::bigint AS trials,
  count(*) FILTER (
    WHERE EXISTS (
      SELECT 1 FROM orders o
      WHERE o.user_id = claims.user_id
        AND o.paid_at IS NOT NULL
        AND o.paid_at > claims.claimed_at
        AND o.operation IN ('purchase', 'renewal', 'extension', 'upgrade')
        -- A zero-value order is a free grant rather than a conversion. Counting
        -- one would make a promotion that gives a month away look like revenue.
        AND o.subtotal_minor - o.discount_minor > 0
    )
  )::bigint AS converted
FROM claims;

-- Payment health per provider.
--
-- The question is "has an acquirer started failing", and answering it needs two
-- rates rather than one, because a customer abandoning a checkout and a gateway
-- refusing a card are not the same event and only the second is the provider's.
--
--   settlement rate  = settled / (settled + failed)
--   completion rate  = settled / (settled + failed + abandoned)
--
-- Intents that are still open are counted separately and appear in neither
-- denominator. One created five minutes ago has not failed, and including it
-- would make today's rate look terrible exactly when somebody is watching.

-- name: PaymentHealthByProvider :many
WITH settlement AS (
  -- The moment the intent actually settled, from the event rather than from
  -- `updated_at`: a later reconciliation touches the row and would inflate
  -- every latency figure it touched.
  SELECT payment_intent_id, min(occurred_at) AS settled_at
  FROM payment_events
  WHERE status = 'succeeded'
  GROUP BY payment_intent_id
)
SELECT
  pi.provider,
  pi.currency,
  count(*)::bigint AS intents,
  count(*) FILTER (WHERE pi.status = 'succeeded')::bigint AS settled,
  count(*) FILTER (WHERE pi.status = 'failed')::bigint AS failed,
  count(*) FILTER (WHERE pi.status IN ('cancelled', 'expired'))::bigint AS abandoned,
  count(*) FILTER (
    WHERE pi.status IN ('pending', 'requires_action', 'processing')
  )::bigint AS still_open,
  COALESCE(sum(pi.amount_minor) FILTER (WHERE pi.status = 'succeeded'), 0)::bigint
    AS settled_minor,
  -- NULL for an intent that never settled, and percentile_cont ignores NULLs,
  -- so these describe the ones that did.
  COALESCE(round(percentile_cont(0.5) WITHIN GROUP (
    ORDER BY extract(epoch FROM (s.settled_at - pi.created_at))
  )), 0)::bigint AS median_settle_seconds,
  COALESCE(round(percentile_cont(0.95) WITHIN GROUP (
    ORDER BY extract(epoch FROM (s.settled_at - pi.created_at))
  )), 0)::bigint AS p95_settle_seconds,
  -- The age of the oldest intent still waiting, which is the stuck-payment
  -- queue expressed as a number rather than as a growing list.
  COALESCE(round(extract(epoch FROM (
    now() - min(pi.created_at) FILTER (
      WHERE pi.status IN ('pending', 'requires_action', 'processing')
    )
  ))), 0)::bigint AS oldest_open_seconds
FROM payment_intents pi
LEFT JOIN settlement s ON s.payment_intent_id = pi.id
WHERE pi.created_at >= sqlc.arg(since) AND pi.created_at < sqlc.arg(until)
GROUP BY pi.provider, pi.currency
ORDER BY pi.provider, pi.currency;

-- name: PaymentHealthByDay :many
-- The same outcomes per day, which is what turns "our settlement rate is 80%"
-- into "it was 97% until Tuesday".
SELECT
  (date_trunc('day', pi.created_at AT TIME ZONE sqlc.arg(timezone)::text))::date AS day,
  pi.provider,
  count(*)::bigint AS intents,
  count(*) FILTER (WHERE pi.status = 'succeeded')::bigint AS settled,
  count(*) FILTER (WHERE pi.status = 'failed')::bigint AS failed
FROM payment_intents pi
WHERE pi.created_at >= sqlc.arg(since) AND pi.created_at < sqlc.arg(until)
GROUP BY 1, pi.provider
ORDER BY 1, pi.provider;

-- name: WebhookHealthByProvider :many
-- Webhook intake beside settlement, because the two fail independently: a
-- gateway that takes the money and cannot deliver the notification looks
-- healthy from the customer's side and produces a stuck queue on ours.
SELECT
  provider,
  count(*)::bigint AS received,
  count(*) FILTER (WHERE NOT signature_valid)::bigint AS rejected,
  count(*) FILTER (WHERE status = 'failed')::bigint AS failed,
  count(*) FILTER (WHERE status = 'processed')::bigint AS processed
FROM provider_webhook_events
WHERE received_at >= sqlc.arg(since) AND received_at < sqlc.arg(until)
GROUP BY provider
ORDER BY provider;

-- name: CustomersByRemnawaveIDs :many
-- Resolves Remnawave user identifiers back to Omniflow customers for the traffic
-- report.
--
-- Consumption itself is never stored here — Remnawave owns traffic, and this
-- repository has no column for a byte a customer used. What Omniflow can add to
-- a list of heavy users is who they are, which is exactly this join and nothing
-- more.
SELECT DISTINCT ON (s.remnawave_user_id)
  s.remnawave_user_id,
  s.user_id AS customer_id,
  s.label,
  u.status AS customer_status
FROM subscriptions s
JOIN users u ON u.id = s.user_id
WHERE s.remnawave_user_id = ANY(sqlc.arg(remnawave_ids)::bigint[])
ORDER BY s.remnawave_user_id, s.created_at;
