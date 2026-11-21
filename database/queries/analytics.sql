-- Business analytics: the subscription base over time, retention, the
-- economics of acquisition, the purchase funnel, and customer satisfaction.
--
-- The rules of the sales report hold here too. Provider money and wallet
-- credit are never added together, money is reported per currency, and a sale
-- belongs to the moment it was paid.
--
-- An entitlement is one paid period: every order that grants access creates its
-- own, and a renewal marks the one before it `superseded`. That chain is what
-- makes a renewal visible after the fact, and it is why `superseded` is not
-- excluded where the question is "did this period get a successor".

-- name: DeleteSubscriptionSnapshot :exec
DELETE FROM subscription_daily_snapshots WHERE day = sqlc.arg(day)::date;

-- name: InsertSubscriptionSnapshot :execrows
-- The base as it stood at one instant, by plan, billing period, kind, and
-- currency.
--
-- One subscription counts once: where an upgrade or an early renewal left two
-- entitlements covering the instant, the one that started last is the one that
-- was in force. A refunded order is out, and so is an entitlement that never
-- reached Remnawave.
--
-- Monthly recurring revenue normalises the price actually charged — net of
-- discount, wallet included, because the wallet balance was money once — to a
-- thirty-day month by the plan version's length. An imported period has no
-- price here, so its figure is the list price for the same version and
-- currency, and the kind keeps it apart as an estimate.
INSERT INTO subscription_daily_snapshots (day, plan_code, billing_period, kind, currency, active, mrr_minor, taken_at)
SELECT
  sqlc.arg(day)::date,
  covering.plan_code,
  covering.billing_period,
  covering.kind,
  CASE WHEN covering.kind IN ('paid', 'imported') THEN covering.currency ELSE '' END,
  count(*)::integer,
  COALESCE(sum(CASE
    WHEN covering.kind = 'paid' THEN covering.net_minor * 2592000 / GREATEST(covering.duration_seconds, 86400)
    WHEN covering.kind = 'imported' THEN covering.list_minor * 2592000 / GREATEST(covering.duration_seconds, 86400)
    ELSE 0
  END), 0)::bigint,
  now()
FROM (
  SELECT DISTINCT ON (COALESCE(e.subscription_id, e.id))
    p.code AS plan_code,
    pv.billing_period,
    pv.duration_seconds,
    o.currency,
    (o.subtotal_minor - o.discount_minor) AS net_minor,
    COALESCE(pp.amount_minor, 0) AS list_minor,
    CASE
      WHEN tc.order_id IS NOT NULL THEN 'trial'
      WHEN o.idempotency_key LIKE 'bedolaga:%' THEN 'imported'
      WHEN o.subtotal_minor - o.discount_minor > 0 THEN 'paid'
      ELSE 'free'
    END AS kind
  FROM entitlements e
  JOIN orders o ON o.id = e.order_id
  JOIN plan_versions pv ON pv.id = e.plan_version_id
  JOIN plans p ON p.id = pv.plan_id
  LEFT JOIN trial_claims tc ON tc.order_id = o.id
  LEFT JOIN plan_prices pp ON pp.plan_version_id = pv.id AND pp.currency = o.currency
  WHERE e.starts_at <= sqlc.arg(at)::timestamptz
    AND e.ends_at > sqlc.arg(at)::timestamptz
    AND e.status NOT IN ('pending', 'failed')
    AND o.paid_at IS NOT NULL AND o.paid_at <= sqlc.arg(at)::timestamptz
    AND o.state <> 'refunded'
  ORDER BY COALESCE(e.subscription_id, e.id), e.starts_at DESC
) covering
GROUP BY covering.plan_code, covering.billing_period, covering.kind,
  CASE WHEN covering.kind IN ('paid', 'imported') THEN covering.currency ELSE '' END;

-- name: MarkSubscriptionSnapshotDay :exec
INSERT INTO subscription_snapshot_days (day, taken_at)
VALUES (sqlc.arg(day)::date, now())
ON CONFLICT (day) DO UPDATE SET taken_at = now();

-- name: SubscriptionSnapshotDaysTaken :many
SELECT day FROM subscription_snapshot_days
WHERE day >= sqlc.arg(from_day)::date AND day <= sqlc.arg(to_day)::date
ORDER BY day;

-- name: SubscriptionSnapshotsBetween :many
SELECT day, plan_code, billing_period, kind, currency, active, mrr_minor
FROM subscription_daily_snapshots
WHERE day >= sqlc.arg(from_day)::date AND day <= sqlc.arg(to_day)::date
ORDER BY day, kind, currency, plan_code, billing_period;

-- name: SubscriptionRenewals :many
-- Of the paid periods that ended in the window, how many got a successor.
--
-- A period counts as renewed when a paid entitlement for the same subscription
-- (the same customer, for one with no subscription) started after it and no
-- later than its end plus the grace allowed. One that ended within the grace
-- and has no successor yet is `pending`, not churned: the customer may still
-- come back, and calling them lost a day after expiry would make every recent
-- window look worse than it is.
--
-- Trials, free grants, and refunded orders are not periods anybody paid for
-- and are left out. An imported period counts: the customer paid for it in the
-- previous bot, and whether they renew here is exactly the question after a
-- migration.
WITH ended AS (
  SELECT e.id, e.user_id, e.subscription_id, e.starts_at, e.ends_at,
    p.code AS plan_code, pv.billing_period,
    CASE WHEN o.idempotency_key LIKE 'bedolaga:%' THEN 'imported' ELSE 'paid' END AS kind
  FROM entitlements e
  JOIN orders o ON o.id = e.order_id
  JOIN plan_versions pv ON pv.id = e.plan_version_id
  JOIN plans p ON p.id = pv.plan_id
  LEFT JOIN trial_claims tc ON tc.order_id = o.id
  WHERE e.ends_at >= sqlc.arg(since)::timestamptz AND e.ends_at < sqlc.arg(until)::timestamptz
    AND e.ends_at <= now()
    AND e.status NOT IN ('pending', 'failed')
    AND o.paid_at IS NOT NULL AND o.state <> 'refunded'
    AND tc.order_id IS NULL
    AND (o.subtotal_minor - o.discount_minor > 0 OR o.idempotency_key LIKE 'bedolaga:%')
), classified AS (
  SELECT ended.*, EXISTS (
    SELECT 1 FROM entitlements successor
    JOIN orders so ON so.id = successor.order_id
    WHERE successor.id <> ended.id
      AND (
        (ended.subscription_id IS NOT NULL AND successor.subscription_id = ended.subscription_id)
        OR (ended.subscription_id IS NULL AND successor.user_id = ended.user_id)
      )
      AND successor.starts_at > ended.starts_at
      AND successor.starts_at <= ended.ends_at + make_interval(secs => sqlc.arg(grace_seconds)::double precision)
      AND successor.status NOT IN ('pending', 'failed')
      AND so.paid_at IS NOT NULL AND so.state <> 'refunded'
      AND so.subtotal_minor - so.discount_minor > 0
  ) AS renewed
  FROM ended
)
SELECT
  classified.plan_code,
  classified.billing_period,
  classified.kind,
  count(*)::bigint AS ended,
  count(*) FILTER (WHERE classified.renewed)::bigint AS renewed,
  count(*) FILTER (
    WHERE NOT classified.renewed
      AND classified.ends_at > now() - make_interval(secs => sqlc.arg(grace_seconds)::double precision)
  )::bigint AS pending
FROM classified
GROUP BY classified.plan_code, classified.billing_period, classified.kind
ORDER BY ended DESC, classified.plan_code;

-- name: AutoRenewCycles :many
-- Automatic renewal by funding source, one row per billing cycle that started
-- in the window. A cycle is the set of attempts for one renewal: it succeeded
-- on the first try, was recovered by a retry, was lost when the attempts ran
-- out, or is still being retried.
WITH cycles AS (
  SELECT
    d.cycle_key,
    ((array_agg(d.funding ORDER BY d.attempt))[1])::text AS funding,
    min(d.scheduled_for) AS started,
    min(d.attempt) FILTER (WHERE d.outcome = 'succeeded') AS success_attempt,
    bool_or(d.outcome = 'scheduled') AS open
  FROM dunning_attempts d
  GROUP BY d.cycle_key
)
SELECT
  cycles.funding::text AS funding,
  count(*)::bigint AS cycles,
  count(*) FILTER (WHERE cycles.success_attempt = 1)::bigint AS first_try,
  count(*) FILTER (WHERE cycles.success_attempt > 1)::bigint AS recovered,
  count(*) FILTER (WHERE cycles.success_attempt IS NULL AND NOT cycles.open)::bigint AS lost,
  count(*) FILTER (WHERE cycles.success_attempt IS NULL AND cycles.open)::bigint AS in_progress
FROM cycles
WHERE cycles.started >= sqlc.arg(since)::timestamptz AND cycles.started < sqlc.arg(until)::timestamptz
GROUP BY cycles.funding
ORDER BY cycles.funding;

-- name: AutoRenewFailureCodes :many
SELECT COALESCE(d.failure_code, 'unknown')::text AS failure_code, count(*)::bigint AS attempts
FROM dunning_attempts d
WHERE d.outcome IN ('failed', 'abandoned')
  AND COALESCE(d.occurred_at, d.scheduled_for) >= sqlc.arg(since)::timestamptz
  AND COALESCE(d.occurred_at, d.scheduled_for) < sqlc.arg(until)::timestamptz
GROUP BY 1
ORDER BY attempts DESC, failure_code
LIMIT 10;

-- name: AutoRenewEnabledCount :one
SELECT count(*)::bigint FROM auto_renew_settings WHERE enabled;

-- name: PayerCohortSizes :many
-- Customers by the month of their first payment, in the installation's
-- timezone. A payment is money from outside: a settled order with a provider
-- amount, or a payment the previous bot took.
WITH payments AS (
  SELECT o.user_id, o.paid_at AS moment FROM orders o
  WHERE o.paid_at IS NOT NULL AND o.paid_minor > 0
  UNION ALL
  SELECT lp.user_id, lp.paid_at FROM legacy_payments lp
), firsts AS (
  SELECT payments.user_id,
    date_trunc('month', min(payments.moment) AT TIME ZONE sqlc.arg(timezone)::text)::date AS cohort
  FROM payments GROUP BY payments.user_id
)
SELECT firsts.cohort, count(*)::bigint AS customers
FROM firsts
WHERE firsts.cohort >= sqlc.arg(from_month)::date
GROUP BY firsts.cohort
ORDER BY firsts.cohort;

-- name: PayerCohortRevenue :many
-- Everything a cohort has paid since its first payment, per currency, net of
-- what was refunded. It is the numerator of lifetime value.
WITH payments AS (
  SELECT o.user_id, o.paid_at AS moment, o.currency, (o.paid_minor - o.refunded_minor) AS amount_minor
  FROM orders o
  WHERE o.paid_at IS NOT NULL AND o.paid_minor > 0
  UNION ALL
  SELECT lp.user_id, lp.paid_at, lp.currency, lp.amount_minor FROM legacy_payments lp
), firsts AS (
  SELECT payments.user_id,
    date_trunc('month', min(payments.moment) AT TIME ZONE sqlc.arg(timezone)::text)::date AS cohort
  FROM payments GROUP BY payments.user_id
)
SELECT firsts.cohort, payments.currency,
  count(DISTINCT payments.user_id)::bigint AS customers,
  COALESCE(sum(payments.amount_minor), 0)::bigint AS revenue_minor
FROM firsts
JOIN payments ON payments.user_id = firsts.user_id
WHERE firsts.cohort >= sqlc.arg(from_month)::date
GROUP BY firsts.cohort, payments.currency
ORDER BY firsts.cohort, payments.currency;

-- name: PayerCohortRetention :many
-- Of each cohort, how many customers held a paid or imported entitlement at
-- some point in each month since their first payment. Access, not payment:
-- a customer on a yearly plan pays once and is retained for twelve months.
-- The previous bot's history arrives as payments without the periods they
-- bought, so a month in which such a payment was taken counts as retained too;
-- without it every migrated cohort would read as lost until the import.
WITH payments AS (
  SELECT o.user_id, o.paid_at AS moment FROM orders o
  WHERE o.paid_at IS NOT NULL AND o.paid_minor > 0
  UNION ALL
  SELECT lp.user_id, lp.paid_at FROM legacy_payments lp
), firsts AS (
  SELECT payments.user_id,
    date_trunc('month', min(payments.moment) AT TIME ZONE sqlc.arg(timezone)::text)::date AS cohort
  FROM payments GROUP BY payments.user_id
), months AS (
  SELECT firsts.user_id, firsts.cohort, offsets.month_offset,
    ((firsts.cohort + make_interval(months => offsets.month_offset))::timestamp AT TIME ZONE sqlc.arg(timezone)::text) AS month_start,
    ((firsts.cohort + make_interval(months => offsets.month_offset + 1))::timestamp AT TIME ZONE sqlc.arg(timezone)::text) AS month_end
  FROM firsts
  CROSS JOIN generate_series(0, sqlc.arg(max_offset)::integer) AS offsets(month_offset)
  WHERE firsts.cohort >= sqlc.arg(from_month)::date
    AND (firsts.cohort + make_interval(months => offsets.month_offset)) <= now()
)
SELECT months.cohort, months.month_offset::integer AS month_offset,
  count(*) FILTER (WHERE EXISTS (
    SELECT 1 FROM entitlements e
    JOIN orders o ON o.id = e.order_id
    WHERE e.user_id = months.user_id
      AND e.starts_at < months.month_end AND e.ends_at > months.month_start
      AND e.status NOT IN ('pending', 'failed')
      AND o.state <> 'refunded'
      AND (o.subtotal_minor - o.discount_minor > 0 OR o.idempotency_key LIKE 'bedolaga:%')
  ) OR EXISTS (
    SELECT 1 FROM legacy_payments lp
    WHERE lp.user_id = months.user_id
      AND lp.paid_at >= months.month_start AND lp.paid_at < months.month_end
  ))::bigint AS active
FROM months
GROUP BY months.cohort, months.month_offset
ORDER BY months.cohort, months.month_offset;

-- name: RevenueAndPayers :many
-- Provider money taken in the window and the customers it came from, per
-- currency: the two halves of revenue per paying customer.
SELECT o.currency,
  COALESCE(sum(o.paid_minor), 0)::bigint AS paid_minor,
  count(DISTINCT o.user_id)::bigint AS payers
FROM orders o
WHERE o.paid_at >= sqlc.arg(since)::timestamptz AND o.paid_at < sqlc.arg(until)::timestamptz
  AND o.paid_minor > 0
GROUP BY o.currency
ORDER BY o.currency;

-- name: InsertNodeTrafficSample :exec
INSERT INTO node_traffic_samples (
  node_uuid, sampled_at, node_name, country_code, connected,
  traffic_used_bytes, traffic_limit_bytes, users_online
)
VALUES (
  sqlc.arg(node_uuid), sqlc.arg(sampled_at), sqlc.arg(node_name), sqlc.arg(country_code),
  sqlc.arg(connected), sqlc.arg(traffic_used_bytes), sqlc.arg(traffic_limit_bytes),
  sqlc.narg(users_online)
)
ON CONFLICT (node_uuid, sampled_at) DO NOTHING;

-- name: PruneNodeTrafficSamples :execrows
DELETE FROM node_traffic_samples WHERE sampled_at < sqlc.arg(before)::timestamptz;

-- name: NodeTrafficDaily :many
-- Traffic each node carried per day, and its busiest hour.
--
-- The node's counter is cumulative within its cycle and drops back when the
-- cycle resets, so a day's traffic is the sum of the positive steps between
-- consecutive samples. A drop to less than half the previous reading is a
-- reset, and the new reading is the traffic since it; a smaller drop is the
-- counter wobbling and counts as nothing, because reading it as a reset would
-- add the whole cycle's traffic again.
WITH stepped AS (
  SELECT s.node_uuid, s.node_name, s.country_code, s.sampled_at, s.users_online,
    s.traffic_limit_bytes,
    s.traffic_used_bytes - lag(s.traffic_used_bytes) OVER w AS delta,
    lag(s.traffic_used_bytes) OVER w AS previous,
    s.traffic_used_bytes
  FROM node_traffic_samples s
  WHERE s.sampled_at >= sqlc.arg(since)::timestamptz - interval '2 hours'
    AND s.sampled_at < sqlc.arg(until)::timestamptz
  WINDOW w AS (PARTITION BY s.node_uuid ORDER BY s.sampled_at)
)
SELECT
  stepped.node_uuid,
  max(stepped.node_name)::text AS node_name,
  max(stepped.country_code)::text AS country_code,
  (date_trunc('day', stepped.sampled_at AT TIME ZONE sqlc.arg(timezone)::text))::date AS day,
  COALESCE(sum(CASE
    WHEN stepped.delta IS NULL THEN 0
    WHEN stepped.delta >= 0 THEN stepped.delta
    WHEN stepped.traffic_used_bytes * 2 < stepped.previous THEN stepped.traffic_used_bytes
    ELSE 0
  END), 0)::bigint AS used_bytes,
  COALESCE(max(stepped.users_online), 0)::integer AS peak_online,
  max(stepped.traffic_limit_bytes)::bigint AS limit_bytes
FROM stepped
WHERE stepped.sampled_at >= sqlc.arg(since)::timestamptz
GROUP BY stepped.node_uuid, 4
ORDER BY 4, node_name;

-- name: CreateAcquisitionSpend :one
-- A retried request with the same idempotency key returns the cost the first
-- one recorded, and `inserted` tells the caller not to audit it twice.
INSERT INTO acquisition_spend (campaign_id, utm_source, spent_on, currency, amount_minor, note, created_by, idempotency_key)
VALUES (
  sqlc.narg(campaign_id), sqlc.narg(utm_source), sqlc.arg(spent_on), sqlc.arg(currency),
  sqlc.arg(amount_minor), sqlc.arg(note), sqlc.arg(created_by), sqlc.narg(idempotency_key)
)
ON CONFLICT (idempotency_key) DO UPDATE SET idempotency_key = EXCLUDED.idempotency_key
RETURNING id, campaign_id, utm_source, spent_on, currency, amount_minor, note, created_by,
  created_at, (xmax = 0)::boolean AS inserted;

-- name: DeleteAcquisitionSpend :execrows
DELETE FROM acquisition_spend WHERE id = sqlc.arg(id);

-- name: ListAcquisitionSpend :many
SELECT s.id, s.campaign_id, s.utm_source, s.spent_on, s.currency, s.amount_minor, s.note,
  s.created_by, s.created_at, COALESCE(c.code, '')::text AS campaign_code,
  COALESCE(c.name, '')::text AS campaign_name
FROM acquisition_spend s
LEFT JOIN ad_campaigns c ON c.id = s.campaign_id
WHERE s.spent_on >= sqlc.arg(from_day)::date AND s.spent_on <= sqlc.arg(to_day)::date
ORDER BY s.spent_on DESC, s.created_at DESC;

-- name: CampaignAcquisitionOutcomes :many
-- Customers a /start campaign brought in during the window, and how many of
-- them have paid since. The revenue they brought is a separate query, per
-- currency.
SELECT c.id AS campaign_id, c.code, c.name,
  count(a.user_id)::bigint AS signups,
  count(a.user_id) FILTER (WHERE EXISTS (
    SELECT 1 FROM orders o
    WHERE o.user_id = a.user_id AND o.paid_at >= a.attributed_at AND o.paid_minor > 0
  ))::bigint AS payers
FROM ad_campaigns c
LEFT JOIN customer_campaign_attributions a ON a.campaign_id = c.id
  AND a.attributed_at >= sqlc.arg(since)::timestamptz AND a.attributed_at < sqlc.arg(until)::timestamptz
GROUP BY c.id, c.code, c.name
ORDER BY signups DESC, c.code;

-- name: CampaignAcquisitionRevenue :many
SELECT a.campaign_id, o.currency,
  COALESCE(sum(o.paid_minor - o.refunded_minor), 0)::bigint AS revenue_minor
FROM customer_campaign_attributions a
JOIN orders o ON o.user_id = a.user_id AND o.paid_at >= a.attributed_at AND o.paid_minor > 0
WHERE a.attributed_at >= sqlc.arg(since)::timestamptz AND a.attributed_at < sqlc.arg(until)::timestamptz
GROUP BY a.campaign_id, o.currency;

-- name: AcquisitionSpendTotals :many
-- Spend in the window by target: a campaign or a `utm_source`.
SELECT s.campaign_id, s.utm_source, s.currency, COALESCE(sum(s.amount_minor), 0)::bigint AS amount_minor
FROM acquisition_spend s
WHERE s.spent_on >= sqlc.arg(from_day)::date AND s.spent_on <= sqlc.arg(to_day)::date
GROUP BY s.campaign_id, s.utm_source, s.currency;

-- name: SourceAcquisition :many
-- Paid orders in the window by the `utm_source` they were attributed to, with
-- how many of their customers paid for the first time with that order.
SELECT lower(oa.utm_source)::text AS utm_source, o.currency,
  count(*)::bigint AS orders,
  count(DISTINCT o.user_id)::bigint AS payers,
  count(DISTINCT o.user_id) FILTER (WHERE NOT EXISTS (
    SELECT 1 FROM orders earlier
    WHERE earlier.user_id = o.user_id AND earlier.paid_at < o.paid_at AND earlier.paid_minor > 0
  ) AND NOT EXISTS (
    SELECT 1 FROM legacy_payments lp WHERE lp.user_id = o.user_id AND lp.paid_at < o.paid_at
  ))::bigint AS new_payers,
  COALESCE(sum(o.paid_minor - o.refunded_minor), 0)::bigint AS revenue_minor
FROM orders o
JOIN order_attributions oa ON oa.order_id = o.id
WHERE o.paid_at >= sqlc.arg(since)::timestamptz AND o.paid_at < sqlc.arg(until)::timestamptz
  AND o.paid_minor > 0
  AND oa.utm_source IS NOT NULL AND oa.utm_source <> ''
GROUP BY 1, o.currency
ORDER BY revenue_minor DESC;

-- name: PromoCodePerformance :many
-- What each promo code cost and what it brought, per currency: the discount
-- given, the provider money from the orders it was used on, and how many of
-- those customers were paying for the first time.
SELECT pc.normalized_code AS code, pr.kind AS promotion_kind, o.currency,
  count(*)::bigint AS redemptions,
  count(DISTINCT r.user_id)::bigint AS customers,
  count(DISTINCT r.user_id) FILTER (WHERE NOT EXISTS (
    SELECT 1 FROM orders earlier
    WHERE earlier.user_id = r.user_id AND earlier.paid_at < o.paid_at AND earlier.paid_minor > 0
  ) AND NOT EXISTS (
    SELECT 1 FROM legacy_payments lp WHERE lp.user_id = r.user_id AND lp.paid_at < o.paid_at
  ))::bigint AS new_customers,
  COALESCE(sum(r.discount_minor), 0)::bigint AS discount_minor,
  COALESCE(sum(o.paid_minor - o.refunded_minor), 0)::bigint AS revenue_minor
FROM promo_redemptions r
JOIN promo_codes pc ON pc.id = r.promo_code_id
JOIN promotions pr ON pr.id = r.promotion_id
JOIN orders o ON o.id = r.order_id
WHERE r.redeemed_at >= sqlc.arg(since)::timestamptz AND r.redeemed_at < sqlc.arg(until)::timestamptz
  AND o.paid_at IS NOT NULL
GROUP BY pc.normalized_code, pr.kind, o.currency
ORDER BY redemptions DESC, code;

-- name: BonusWalletCredit :many
-- Wallet credit given rather than bought, by where it came from. A top-up, a
-- refund to the wallet, and a merge move money a customer already had; every
-- other credit is something the business gave away.
SELECT t.reference_type AS source, e.currency,
  count(DISTINCT t.id)::bigint AS grants,
  COALESCE(sum(e.amount_minor), 0)::bigint AS amount_minor
FROM ledger_entries e
JOIN ledger_transactions t ON t.id = e.transaction_id
WHERE e.account_type = 'customer_wallet' AND e.amount_minor > 0
  AND e.created_at >= sqlc.arg(since)::timestamptz AND e.created_at < sqlc.arg(until)::timestamptz
  AND t.reference_type NOT IN (
    'wallet_topup', 'refund', 'order', 'goods_order', 'order_credit', 'customer_merge',
    'ledger_entry', 'referral_withdrawal'
  )
  AND t.type NOT IN ('payment', 'refund', 'expiration')
GROUP BY t.reference_type, e.currency
ORDER BY amount_minor DESC;

-- name: FreeAccessGranted :many
-- Access given away in the window: trials, and every other zero-value grant by
-- the kind of order that carried it, with the days it covered.
SELECT
  CASE WHEN tc.order_id IS NOT NULL THEN 'trial' ELSE o.operation END::text AS source,
  count(*)::bigint AS grants,
  COALESCE(sum(GREATEST(extract(epoch FROM (e.ends_at - e.starts_at)), 0)) / 86400, 0)::bigint AS days
FROM entitlements e
JOIN orders o ON o.id = e.order_id
LEFT JOIN trial_claims tc ON tc.order_id = o.id
WHERE e.created_at >= sqlc.arg(since)::timestamptz AND e.created_at < sqlc.arg(until)::timestamptz
  AND o.subtotal_minor - o.discount_minor <= 0
  AND o.idempotency_key NOT LIKE 'bedolaga:%'
  AND e.status NOT IN ('pending', 'failed')
GROUP BY 1
ORDER BY grants DESC;

-- name: RecordFunnelEvent :exec
-- The first time only: a customer who looks at the plans every day reached
-- that step once.
INSERT INTO funnel_events (user_id, step, channel)
VALUES (sqlc.arg(user_id), sqlc.arg(step), sqlc.arg(channel))
ON CONFLICT (user_id, step) DO NOTHING;

-- name: MoveCustomerFunnelEvents :exec
-- A customer merge: the survivor reached a step when either account first
-- did. The earlier of the two moments is kept, and the absorbed account's
-- rows go with it.
WITH moved AS (
  DELETE FROM funnel_events f WHERE f.user_id = sqlc.arg(source)
  RETURNING f.step, f.channel, f.occurred_at
)
INSERT INTO funnel_events (user_id, step, channel, occurred_at)
SELECT sqlc.arg(target), moved.step, moved.channel, moved.occurred_at FROM moved
ON CONFLICT (user_id, step) DO UPDATE
SET occurred_at = LEAST(funnel_events.occurred_at, EXCLUDED.occurred_at),
    channel = CASE WHEN EXCLUDED.occurred_at < funnel_events.occurred_at
      THEN EXCLUDED.channel ELSE funnel_events.channel END;

-- name: FunnelSummary :one
-- Customers who signed up in the window, and how far each got by now. It is a
-- cohort measure, like trial conversion, and reads low for a window ending
-- today.
WITH cohort AS (
  SELECT u.id, u.created_at FROM users u
  WHERE u.created_at >= sqlc.arg(since)::timestamptz AND u.created_at < sqlc.arg(until)::timestamptz
    AND u.status <> 'merged'
)
SELECT
  count(*)::bigint AS signups,
  count(*) FILTER (WHERE EXISTS (
    SELECT 1 FROM funnel_events f WHERE f.user_id = cohort.id AND f.step = 'plans_viewed'
  ))::bigint AS plans_viewed,
  count(*) FILTER (WHERE EXISTS (
    SELECT 1 FROM funnel_events f WHERE f.user_id = cohort.id AND f.step = 'checkout_started'
  ))::bigint AS checkout_started,
  count(*) FILTER (WHERE EXISTS (
    SELECT 1 FROM trial_claims tc WHERE tc.user_id = cohort.id AND tc.origin = 'omniflow'
  ))::bigint AS trial_claimed,
  count(*) FILTER (WHERE EXISTS (
    SELECT 1 FROM orders o
    WHERE o.user_id = cohort.id AND o.paid_at IS NOT NULL
      AND o.subtotal_minor - o.discount_minor > 0
      AND o.operation IN ('purchase', 'renewal', 'extension', 'upgrade')
  ))::bigint AS paid
FROM cohort;

-- name: FunnelByChannel :many
-- The two recorded steps by the channel they were first reached on.
SELECT f.step, f.channel, count(*)::bigint AS customers
FROM funnel_events f
JOIN users u ON u.id = f.user_id
WHERE u.created_at >= sqlc.arg(since)::timestamptz AND u.created_at < sqlc.arg(until)::timestamptz
  AND u.status <> 'merged'
GROUP BY f.step, f.channel
ORDER BY f.step, f.channel;

-- name: PendingRatingRequests :many
-- Resolved tickets whose customer has not been asked for a mark yet. Only
-- tickets resolved in the last three days: a question about a conversation a
-- month old is noise, and the first deployment must not ask about every ticket
-- ever closed.
SELECT t.id AS ticket_id, t.user_id, t.subject, t.resolved_at
FROM support_tickets t
WHERE t.status IN ('resolved', 'closed')
  AND t.rating IS NULL AND t.rating_requested_at IS NULL
  AND t.resolved_at IS NOT NULL AND t.resolved_at >= now() - interval '3 days'
  AND t.resolved_at <= now() - make_interval(secs => sqlc.arg(settle_seconds)::double precision)
ORDER BY t.resolved_at
LIMIT sqlc.arg(batch_size);

-- name: MarkRatingRequested :exec
UPDATE support_tickets SET rating_requested_at = now()
WHERE id = sqlc.arg(ticket_id) AND rating_requested_at IS NULL;

-- name: RecordTicketRating :execrows
-- The customer's own ticket, once. A second tap on an old keyboard changes
-- nothing.
UPDATE support_tickets SET rating = sqlc.arg(rating)::smallint, rated_at = now()
WHERE id = sqlc.arg(ticket_id) AND user_id = sqlc.arg(user_id) AND rating IS NULL;

-- name: SupportSatisfaction :one
-- Marks received in the window. CSAT is the share of fours and fives.
SELECT
  count(*)::bigint AS ratings,
  count(*) FILTER (WHERE t.rating >= 4)::bigint AS satisfied,
  COALESCE(avg(t.rating), 0)::double precision AS average,
  count(*) FILTER (WHERE t.rating = 1)::bigint AS r1,
  count(*) FILTER (WHERE t.rating = 2)::bigint AS r2,
  count(*) FILTER (WHERE t.rating = 3)::bigint AS r3,
  count(*) FILTER (WHERE t.rating = 4)::bigint AS r4,
  count(*) FILTER (WHERE t.rating = 5)::bigint AS r5,
  (SELECT count(*) FROM support_tickets asked
    WHERE asked.rating_requested_at >= sqlc.arg(since)::timestamptz
      AND asked.rating_requested_at < sqlc.arg(until)::timestamptz)::bigint AS requested
FROM support_tickets t
WHERE t.rated_at >= sqlc.arg(since)::timestamptz AND t.rated_at < sqlc.arg(until)::timestamptz;

-- name: SupportSatisfactionByOperator :many
-- Marks by the operator the ticket was assigned to when it was rated.
SELECT a.id AS operator_id, a.display_name,
  count(*)::bigint AS ratings,
  count(*) FILTER (WHERE t.rating >= 4)::bigint AS satisfied,
  COALESCE(avg(t.rating), 0)::double precision AS average
FROM support_tickets t
JOIN admin_users a ON a.id = t.assignee_id
WHERE t.rated_at >= sqlc.arg(since)::timestamptz AND t.rated_at < sqlc.arg(until)::timestamptz
GROUP BY a.id, a.display_name
ORDER BY ratings DESC, a.display_name;
