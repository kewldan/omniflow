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
