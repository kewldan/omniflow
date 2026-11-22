-- Unit economics, activation and usage, the movement of recurring revenue,
-- the renewals ahead, why customers leave, and what each acquisition channel
-- is worth.
--
-- The rules of the sales report hold here too: provider money and wallet
-- credit are never added together, money is reported per currency and never
-- converted, and a sale belongs to the moment it was paid.

-- name: CreateOperatingCost :one
-- A retried request with the same idempotency key returns the cost the first
-- one recorded, and `inserted` tells the caller not to audit it twice.
INSERT INTO operating_costs (
  category, node_uuid, covers_from, covers_to, currency, amount_minor, note, created_by, idempotency_key
)
VALUES (
  sqlc.arg(category), sqlc.narg(node_uuid), sqlc.arg(covers_from), sqlc.arg(covers_to),
  sqlc.arg(currency), sqlc.arg(amount_minor), sqlc.arg(note), sqlc.arg(created_by),
  sqlc.narg(idempotency_key)
)
ON CONFLICT (idempotency_key) DO UPDATE SET idempotency_key = EXCLUDED.idempotency_key
RETURNING id, category, node_uuid, covers_from, covers_to, currency, amount_minor, note,
  created_by, created_at, (xmax = 0)::boolean AS inserted;

-- name: DeleteOperatingCost :execrows
DELETE FROM operating_costs WHERE id = sqlc.arg(id);

-- name: ListOperatingCosts :many
-- Every cost whose span touches the window.
SELECT id, category, node_uuid, covers_from, covers_to, currency, amount_minor, note,
  created_by, created_at
FROM operating_costs
WHERE covers_to >= sqlc.arg(from_day)::date AND covers_from <= sqlc.arg(to_day)::date
ORDER BY covers_from DESC, created_at DESC;

-- name: OperatingCostShares :many
-- The part of each cost that falls inside the window. A cost is spread evenly
-- over the days it covers, so a monthly bill read for one week counts a
-- quarter of itself. Rounded to the minor unit per group.
SELECT c.category, COALESCE(c.node_uuid, '')::text AS node_uuid, c.currency,
  COALESCE(round(sum(
    c.amount_minor::numeric
    * (LEAST(c.covers_to, sqlc.arg(to_day)::date) - GREATEST(c.covers_from, sqlc.arg(from_day)::date) + 1)
    / (c.covers_to - c.covers_from + 1)
  )), 0)::bigint AS amount_minor
FROM operating_costs c
WHERE c.covers_to >= sqlc.arg(from_day)::date AND c.covers_from <= sqlc.arg(to_day)::date
GROUP BY c.category, c.node_uuid, c.currency
ORDER BY c.currency, c.category, node_uuid;

-- name: UpsertProviderFee :one
-- One rate per provider and date: entering a rate for a date that already has
-- one corrects it.
INSERT INTO payment_provider_fees (
  provider, effective_from, percent_bp, fixed_minor, fixed_currency, note, created_by
)
VALUES (
  sqlc.arg(provider), sqlc.arg(effective_from), sqlc.arg(percent_bp), sqlc.arg(fixed_minor),
  sqlc.narg(fixed_currency), sqlc.arg(note), sqlc.arg(created_by)
)
ON CONFLICT (provider, effective_from) DO UPDATE SET
  percent_bp = EXCLUDED.percent_bp, fixed_minor = EXCLUDED.fixed_minor,
  fixed_currency = EXCLUDED.fixed_currency, note = EXCLUDED.note,
  created_by = EXCLUDED.created_by, created_at = now()
RETURNING id, provider, effective_from, percent_bp, fixed_minor, fixed_currency, note,
  created_by, created_at;

-- name: DeleteProviderFee :execrows
DELETE FROM payment_provider_fees WHERE id = sqlc.arg(id);

-- name: ListProviderFees :many
SELECT id, provider, effective_from, percent_bp, fixed_minor, fixed_currency, note,
  created_by, created_at
FROM payment_provider_fees
ORDER BY provider, effective_from DESC;

-- name: ProviderMoneyAndFees :many
-- Provider money taken in the window by the provider that took it, with what
-- the provider keeps under the rate in force on the day of each payment. An
-- order paid entirely from the wallet took no provider money and is not here.
-- A payment with no rate entered for its provider and day is counted apart:
-- its fee is unknown, not zero.
WITH paid AS (
  SELECT o.id, o.paid_at, o.currency, o.paid_minor,
    COALESCE((
      SELECT pi.provider FROM payment_intents pi
      WHERE pi.order_id = o.id AND pi.status = 'succeeded'
      ORDER BY pi.updated_at DESC LIMIT 1
    ), 'unknown')::text AS provider
  FROM orders o
  WHERE o.paid_at >= sqlc.arg(since)::timestamptz AND o.paid_at < sqlc.arg(until)::timestamptz
    AND o.paid_minor > 0
)
SELECT paid.provider, paid.currency,
  count(*)::bigint AS payments,
  COALESCE(sum(paid.paid_minor), 0)::bigint AS paid_minor,
  count(*) FILTER (WHERE rate.percent_bp IS NULL)::bigint AS without_rate,
  COALESCE(sum(
    CASE WHEN rate.percent_bp IS NULL THEN 0
    ELSE paid.paid_minor * rate.percent_bp / 10000
      + CASE WHEN rate.fixed_currency = paid.currency THEN rate.fixed_minor ELSE 0 END
    END
  ), 0)::bigint AS fee_minor
FROM paid
LEFT JOIN LATERAL (
  SELECT f.percent_bp, f.fixed_minor, f.fixed_currency
  FROM payment_provider_fees f
  WHERE f.provider = paid.provider
    AND f.effective_from <= (paid.paid_at AT TIME ZONE 'UTC')::date
  ORDER BY f.effective_from DESC
  LIMIT 1
) rate ON true
GROUP BY paid.provider, paid.currency
ORDER BY paid.currency, paid_minor DESC;

-- name: RefundsByCurrency :many
-- Refunds issued in the window, on the day they were issued.
SELECT r.currency, count(*)::bigint AS refunds, COALESCE(sum(r.amount_minor), 0)::bigint AS amount_minor
FROM refunds r
WHERE r.created_at >= sqlc.arg(since)::timestamptz AND r.created_at < sqlc.arg(until)::timestamptz
  AND r.status = 'succeeded'
GROUP BY r.currency
ORDER BY r.currency;

-- name: ReferralRewardCosts :many
-- What the referral programme gave in the window, by role, net of rewards
-- already reversed: money credited to wallets, and days of access. Money is a
-- cost when it is credited, not when it is withdrawn; a withdrawal only moves
-- what was already earned.
SELECT r.role, r.currency,
  count(*)::bigint AS rewards,
  COALESCE(sum(r.amount_minor), 0)::bigint AS amount_minor,
  COALESCE(sum(r.days), 0)::bigint AS days
FROM referral_rewards r
WHERE r.granted_at >= sqlc.arg(since)::timestamptz AND r.granted_at < sqlc.arg(until)::timestamptz
  AND r.reversed_at IS NULL
GROUP BY r.role, r.currency
ORDER BY r.currency, r.role;

-- name: ReferralOutcomes :one
-- Customers invited in the window, how many qualified, and how many have paid
-- since they were invited.
SELECT
  count(*)::bigint AS invited,
  count(*) FILTER (WHERE ra.qualified_at IS NOT NULL)::bigint AS qualified,
  count(*) FILTER (WHERE ra.review_state = 'rejected')::bigint AS rejected,
  count(*) FILTER (WHERE EXISTS (
    SELECT 1 FROM orders o
    WHERE o.user_id = ra.referred_user_id AND o.paid_at >= ra.created_at AND o.paid_minor > 0
  ))::bigint AS payers
FROM referral_attributions ra
WHERE ra.created_at >= sqlc.arg(since)::timestamptz AND ra.created_at < sqlc.arg(until)::timestamptz;

-- name: ReferralRevenue :many
-- What the customers invited in the window have paid since, per currency.
SELECT o.currency, COALESCE(sum(o.paid_minor - o.refunded_minor), 0)::bigint AS revenue_minor
FROM referral_attributions ra
JOIN orders o ON o.user_id = ra.referred_user_id AND o.paid_at >= ra.created_at AND o.paid_minor > 0
WHERE ra.created_at >= sqlc.arg(since)::timestamptz AND ra.created_at < sqlc.arg(until)::timestamptz
  AND ra.review_state <> 'rejected'
GROUP BY o.currency
ORDER BY o.currency;

-- name: PayerChannelValue :many
-- The customers who paid for the first time in the window, by the channel
-- that brought them, with what they have paid since, per currency.
--
-- The channel is the first touch Omniflow knows of, in this order: a /start
-- campaign, an invitation, the `utm_source` of the first paid order, and
-- otherwise organic. A customer is in one channel only, so the channels add up
-- to the whole cohort. Months is the time each customer has had to pay since
-- their first payment, at least one: lifetime value per month is what payback
-- divides by.
WITH payments AS (
  SELECT o.user_id, o.paid_at AS moment FROM orders o
  WHERE o.paid_at IS NOT NULL AND o.paid_minor > 0
  UNION ALL
  SELECT lp.user_id, lp.paid_at FROM legacy_payments lp
), firsts AS (
  SELECT payments.user_id, min(payments.moment) AS first_paid
  FROM payments GROUP BY payments.user_id
), cohort AS (
  SELECT firsts.user_id, firsts.first_paid,
    GREATEST(extract(epoch FROM (now() - firsts.first_paid)) / 2592000, 1)::double precision AS months,
    COALESCE(
      (SELECT 'campaign:' || c.code FROM customer_campaign_attributions a
        JOIN ad_campaigns c ON c.id = a.campaign_id
        WHERE a.user_id = firsts.user_id ORDER BY a.attributed_at LIMIT 1),
      (SELECT 'referral' FROM referral_attributions ra
        WHERE ra.referred_user_id = firsts.user_id AND ra.review_state <> 'rejected' LIMIT 1),
      (SELECT 'utm:' || lower(oa.utm_source) FROM orders fo
        JOIN order_attributions oa ON oa.order_id = fo.id
        WHERE fo.user_id = firsts.user_id AND fo.paid_minor > 0
          AND oa.utm_source IS NOT NULL AND oa.utm_source <> ''
        ORDER BY fo.paid_at LIMIT 1),
      'organic'
    )::text AS channel
  FROM firsts
  WHERE firsts.first_paid >= sqlc.arg(since)::timestamptz AND firsts.first_paid < sqlc.arg(until)::timestamptz
), per_customer AS (
  SELECT cohort.user_id, cohort.channel, cohort.months, o.currency,
    sum(o.paid_minor - o.refunded_minor) AS revenue_minor
  FROM cohort
  JOIN orders o ON o.user_id = cohort.user_id AND o.paid_at IS NOT NULL AND o.paid_minor > 0
  GROUP BY cohort.user_id, cohort.channel, cohort.months, o.currency
)
SELECT per_customer.channel, per_customer.currency,
  count(*)::bigint AS payers,
  COALESCE(sum(per_customer.revenue_minor), 0)::bigint AS revenue_minor,
  COALESCE(sum(per_customer.months), 0)::double precision AS months
FROM per_customer
GROUP BY per_customer.channel, per_customer.currency
ORDER BY per_customer.channel, per_customer.currency;

-- name: FunnelByLocale :many
-- The funnel's two ends by the customer's interface language: of those who
-- signed up in the window, how many claimed a trial and how many paid.
SELECT u.locale,
  count(*)::bigint AS signups,
  count(*) FILTER (WHERE EXISTS (
    SELECT 1 FROM trial_claims tc WHERE tc.user_id = u.id AND tc.origin = 'omniflow'
  ))::bigint AS trial_claimed,
  count(*) FILTER (WHERE EXISTS (
    SELECT 1 FROM orders o
    WHERE o.user_id = u.id AND o.paid_at IS NOT NULL
      AND o.subtotal_minor - o.discount_minor > 0
      AND o.operation IN ('purchase', 'renewal', 'extension', 'upgrade')
  ))::bigint AS paid
FROM users u
WHERE u.created_at >= sqlc.arg(since)::timestamptz AND u.created_at < sqlc.arg(until)::timestamptz
  AND u.status <> 'merged'
GROUP BY u.locale
ORDER BY signups DESC, u.locale;

-- name: RecurringRevenueMovement :many
-- How recurring revenue moved between the start and the end of the window,
-- per customer and currency: new, returning after a gap, grown, shrunk, and
-- lost. Monthly revenue is valued as the daily snapshot values it — the price
-- charged net of discount, normalised to thirty days, and an imported period
-- at list price — so the two ends match the revenue trend above it.
--
-- A customer counts as returning rather than new when they paid for anything
-- before the window, here or in the previous bot.
WITH instants AS (
  SELECT 'start'::text AS side, sqlc.arg(since)::timestamptz AS at
  UNION ALL
  SELECT 'end'::text, LEAST(sqlc.arg(until)::timestamptz, now())
), covering AS (
  SELECT DISTINCT ON (instants.side, COALESCE(e.subscription_id, e.id))
    instants.side, e.user_id, o.currency,
    (CASE WHEN o.idempotency_key LIKE 'bedolaga:%' THEN COALESCE(pp.amount_minor, 0)
      ELSE o.subtotal_minor - o.discount_minor END)
      * 2592000 / GREATEST(pv.duration_seconds, 86400) AS mrr
  FROM instants
  JOIN entitlements e ON e.starts_at <= instants.at AND e.ends_at > instants.at
  JOIN orders o ON o.id = e.order_id
  JOIN plan_versions pv ON pv.id = e.plan_version_id
  LEFT JOIN trial_claims tc ON tc.order_id = o.id
  LEFT JOIN plan_prices pp ON pp.plan_version_id = pv.id AND pp.currency = o.currency
  WHERE e.status NOT IN ('pending', 'failed')
    AND o.paid_at IS NOT NULL AND o.paid_at <= instants.at
    AND o.state <> 'refunded'
    AND tc.order_id IS NULL
    AND (o.subtotal_minor - o.discount_minor > 0 OR o.idempotency_key LIKE 'bedolaga:%')
  ORDER BY instants.side, COALESCE(e.subscription_id, e.id), e.starts_at DESC
), per_customer AS (
  SELECT covering.user_id, covering.currency,
    COALESCE(sum(covering.mrr) FILTER (WHERE covering.side = 'start'), 0)::bigint AS start_mrr,
    COALESCE(sum(covering.mrr) FILTER (WHERE covering.side = 'end'), 0)::bigint AS end_mrr
  FROM covering
  GROUP BY covering.user_id, covering.currency
), classified AS (
  SELECT per_customer.*,
    (per_customer.start_mrr = 0 AND (
      EXISTS (SELECT 1 FROM orders earlier WHERE earlier.user_id = per_customer.user_id
        AND earlier.paid_minor > 0 AND earlier.paid_at < sqlc.arg(since)::timestamptz)
      OR EXISTS (SELECT 1 FROM legacy_payments lp WHERE lp.user_id = per_customer.user_id
        AND lp.paid_at < sqlc.arg(since)::timestamptz)
    )) AS returning
  FROM per_customer
)
SELECT classified.currency,
  COALESCE(sum(classified.start_mrr), 0)::bigint AS start_mrr,
  COALESCE(sum(classified.end_mrr), 0)::bigint AS end_mrr,
  count(*) FILTER (WHERE classified.start_mrr > 0)::bigint AS start_customers,
  count(*) FILTER (WHERE classified.end_mrr > 0)::bigint AS end_customers,
  COALESCE(sum(classified.end_mrr) FILTER (WHERE classified.start_mrr = 0 AND NOT classified.returning), 0)::bigint AS new_mrr,
  count(*) FILTER (WHERE classified.start_mrr = 0 AND NOT classified.returning)::bigint AS new_customers,
  COALESCE(sum(classified.end_mrr) FILTER (WHERE classified.start_mrr = 0 AND classified.returning), 0)::bigint AS reactivated_mrr,
  count(*) FILTER (WHERE classified.start_mrr = 0 AND classified.returning)::bigint AS reactivated_customers,
  COALESCE(sum(classified.end_mrr - classified.start_mrr) FILTER (WHERE classified.start_mrr > 0 AND classified.end_mrr > classified.start_mrr), 0)::bigint AS expansion_mrr,
  count(*) FILTER (WHERE classified.start_mrr > 0 AND classified.end_mrr > classified.start_mrr)::bigint AS expansion_customers,
  COALESCE(sum(classified.start_mrr - classified.end_mrr) FILTER (WHERE classified.end_mrr > 0 AND classified.end_mrr < classified.start_mrr), 0)::bigint AS contraction_mrr,
  count(*) FILTER (WHERE classified.end_mrr > 0 AND classified.end_mrr < classified.start_mrr)::bigint AS contraction_customers,
  COALESCE(sum(classified.start_mrr) FILTER (WHERE classified.start_mrr > 0 AND classified.end_mrr = 0), 0)::bigint AS churned_mrr,
  count(*) FILTER (WHERE classified.start_mrr > 0 AND classified.end_mrr = 0)::bigint AS churned_customers
FROM classified
GROUP BY classified.currency
ORDER BY classified.currency;

-- name: RenewalForecast :many
-- The paid periods that end in the coming days and have no successor yet, by
-- plan and currency: how many, how many will renew themselves, and what
-- renewing them all would bring at today's price of the same plan version
-- (what was paid, where that version has no price in the currency).
WITH ending AS (
  SELECT DISTINCT ON (COALESCE(e.subscription_id, e.id))
    e.id, e.user_id, e.subscription_id, e.starts_at, e.ends_at, e.plan_version_id,
    o.currency, (o.subtotal_minor - o.discount_minor) AS net_minor
  FROM entitlements e
  JOIN orders o ON o.id = e.order_id
  LEFT JOIN trial_claims tc ON tc.order_id = o.id
  WHERE e.ends_at > now() AND e.ends_at <= now() + make_interval(days => sqlc.arg(horizon_days)::integer)
    AND e.status NOT IN ('pending', 'failed')
    AND o.paid_at IS NOT NULL AND o.state <> 'refunded'
    AND tc.order_id IS NULL
    AND (o.subtotal_minor - o.discount_minor > 0 OR o.idempotency_key LIKE 'bedolaga:%')
  ORDER BY COALESCE(e.subscription_id, e.id), e.ends_at DESC
), open_periods AS (
  SELECT ending.* FROM ending
  WHERE NOT EXISTS (
    SELECT 1 FROM entitlements successor
    JOIN orders so ON so.id = successor.order_id
    WHERE successor.id <> ending.id
      AND (
        (ending.subscription_id IS NOT NULL AND successor.subscription_id = ending.subscription_id)
        OR (ending.subscription_id IS NULL AND successor.user_id = ending.user_id)
      )
      AND successor.starts_at > ending.starts_at
      AND successor.status NOT IN ('pending', 'failed')
      AND so.paid_at IS NOT NULL AND so.state <> 'refunded'
  )
)
SELECT p.code AS plan_code, pv.billing_period, open_periods.currency,
  count(*)::bigint AS due,
  count(*) FILTER (WHERE EXISTS (
    SELECT 1 FROM auto_renew_settings ar
    WHERE ar.user_id = open_periods.user_id AND ar.enabled
      AND (ar.subscription_id IS NULL OR ar.subscription_id = open_periods.subscription_id)
  ))::bigint AS auto_renew,
  COALESCE(sum(COALESCE(pp.amount_minor, open_periods.net_minor)), 0)::bigint AS value_minor
FROM open_periods
JOIN plan_versions pv ON pv.id = open_periods.plan_version_id
JOIN plans p ON p.id = pv.plan_id
LEFT JOIN plan_prices pp ON pp.plan_version_id = pv.id AND pp.currency = open_periods.currency
GROUP BY p.code, pv.billing_period, open_periods.currency
ORDER BY value_minor DESC, p.code;

-- name: RecordChurnFeedback :execrows
-- The customer's answer for one occasion. A subscription that is not theirs is
-- refused by writing nothing; answering again the same day replaces the answer.
INSERT INTO churn_feedback (user_id, subscription_id, occasion, reason, surface)
SELECT sqlc.arg(user_id)::uuid, sqlc.narg(subscription_id)::uuid, sqlc.arg(occasion)::text,
  sqlc.arg(reason)::text, sqlc.arg(surface)::text
WHERE sqlc.narg(subscription_id)::uuid IS NULL OR EXISTS (
  SELECT 1 FROM subscriptions s
  WHERE s.id = sqlc.narg(subscription_id)::uuid AND s.user_id = sqlc.arg(user_id)::uuid
)
ON CONFLICT (user_id, occasion, answered_on, (COALESCE(subscription_id, '00000000-0000-0000-0000-000000000000'::uuid)))
DO UPDATE SET reason = EXCLUDED.reason, surface = EXCLUDED.surface, answered_at = now();

-- name: ChurnReasons :many
SELECT f.occasion, f.reason, count(*)::bigint AS answers
FROM churn_feedback f
WHERE f.answered_at >= sqlc.arg(since)::timestamptz AND f.answered_at < sqlc.arg(until)::timestamptz
GROUP BY f.occasion, f.reason
ORDER BY f.occasion, answers DESC, f.reason;

-- name: MoveCustomerChurnFeedback :exec
-- A customer merge: the survivor keeps the absorbed account's answers. Where
-- both answered for the same occasion on the same day, the survivor's stands.
UPDATE churn_feedback f SET user_id = sqlc.arg(target)
WHERE f.user_id = sqlc.arg(source)
  AND NOT EXISTS (
    SELECT 1 FROM churn_feedback kept
    WHERE kept.user_id = sqlc.arg(target) AND kept.occasion = f.occasion
      AND kept.answered_on = f.answered_on
      AND COALESCE(kept.subscription_id, '00000000-0000-0000-0000-000000000000'::uuid)
        = COALESCE(f.subscription_id, '00000000-0000-0000-0000-000000000000'::uuid)
  );

-- name: UpsertVpnUsage :exec
-- One page of what Remnawave reported. The first connection is the earliest
-- ever seen, so a later reading never moves it forward; the rest is the
-- latest reading, except that a user who has gone quiet keeps the last node
-- and online moment they had.
INSERT INTO vpn_usage_observations (
  remnawave_user_id, first_connected_at, online_at, last_node_uuid, active_devices, device_limit, observed_at
)
SELECT reading.remnawave_user_id, reading.first_connected_at, reading.online_at,
  NULLIF(reading.last_node_uuid, ''), reading.active_devices,
  CASE WHEN reading.device_limit < 0 THEN NULL ELSE reading.device_limit END, now()
FROM (
  SELECT
    unnest(sqlc.arg(remnawave_user_ids)::bigint[]) AS remnawave_user_id,
    unnest(sqlc.arg(first_connected_ats)::timestamptz[]) AS first_connected_at,
    unnest(sqlc.arg(online_ats)::timestamptz[]) AS online_at,
    unnest(sqlc.arg(last_node_uuids)::text[]) AS last_node_uuid,
    unnest(sqlc.arg(active_devices)::integer[]) AS active_devices,
    unnest(sqlc.arg(device_limits)::integer[]) AS device_limit
) AS reading
ON CONFLICT (remnawave_user_id) DO UPDATE SET
  first_connected_at = LEAST(vpn_usage_observations.first_connected_at, EXCLUDED.first_connected_at),
  online_at = COALESCE(EXCLUDED.online_at, vpn_usage_observations.online_at),
  last_node_uuid = COALESCE(EXCLUDED.last_node_uuid, vpn_usage_observations.last_node_uuid),
  active_devices = EXCLUDED.active_devices,
  device_limit = EXCLUDED.device_limit,
  observed_at = EXCLUDED.observed_at;

-- name: RecordFirstConnection :exec
-- Remnawave's own first-connection event, ahead of the next census.
INSERT INTO vpn_usage_observations (remnawave_user_id, first_connected_at, online_at, observed_at)
VALUES (sqlc.arg(remnawave_user_id), sqlc.arg(connected_at), sqlc.arg(connected_at), now())
ON CONFLICT (remnawave_user_id) DO UPDATE SET
  first_connected_at = LEAST(vpn_usage_observations.first_connected_at, EXCLUDED.first_connected_at),
  online_at = GREATEST(vpn_usage_observations.online_at, EXCLUDED.online_at);

-- name: UsageObservedAt :one
-- When the census last ran, or null before its first pass.
SELECT max(observed_at)::timestamptz AS observed_at FROM vpn_usage_observations;

-- name: TrialActivation :one
-- Of the trials claimed in the window, how many customers connected, how soon,
-- and how connecting relates to paying afterwards. A customer's connection is
-- the earliest across every Remnawave user they hold.
WITH usage AS (
  SELECT COALESCE(s.user_id, ru.user_id) AS user_id, min(v.first_connected_at) AS first_connected_at
  FROM vpn_usage_observations v
  LEFT JOIN subscriptions s ON s.remnawave_user_id = v.remnawave_user_id
  LEFT JOIN remnawave_users ru ON ru.remnawave_id = v.remnawave_user_id
  GROUP BY 1
), claims AS (
  SELECT tc.user_id, tc.claimed_at, usage.first_connected_at,
    EXISTS (
      SELECT 1 FROM orders o
      WHERE o.user_id = tc.user_id AND o.paid_at >= tc.claimed_at AND o.paid_minor > 0
    ) AS paid
  FROM trial_claims tc
  LEFT JOIN usage ON usage.user_id = tc.user_id
  WHERE tc.claimed_at >= sqlc.arg(since)::timestamptz AND tc.claimed_at < sqlc.arg(until)::timestamptz
    AND tc.origin = 'omniflow'
)
SELECT
  count(*)::bigint AS trials,
  count(*) FILTER (WHERE claims.first_connected_at IS NOT NULL)::bigint AS connected,
  count(*) FILTER (WHERE claims.first_connected_at IS NOT NULL
    AND claims.first_connected_at <= claims.claimed_at + interval '1 day')::bigint AS connected_within_day,
  count(*) FILTER (WHERE claims.paid AND claims.first_connected_at IS NOT NULL)::bigint AS connected_paid,
  count(*) FILTER (WHERE claims.paid AND claims.first_connected_at IS NULL)::bigint AS unconnected_paid,
  COALESCE(percentile_cont(0.5) WITHIN GROUP (
    ORDER BY extract(epoch FROM (claims.first_connected_at - claims.claimed_at)) / 3600
  ) FILTER (WHERE claims.first_connected_at >= claims.claimed_at), -1)::double precision AS median_hours
FROM claims;

-- name: PurchaseActivation :one
-- Of the customers whose first payment fell in the window, how many have
-- connected, and how many within a day of paying.
WITH usage AS (
  SELECT COALESCE(s.user_id, ru.user_id) AS user_id, min(v.first_connected_at) AS first_connected_at
  FROM vpn_usage_observations v
  LEFT JOIN subscriptions s ON s.remnawave_user_id = v.remnawave_user_id
  LEFT JOIN remnawave_users ru ON ru.remnawave_id = v.remnawave_user_id
  GROUP BY 1
), payments AS (
  SELECT o.user_id, o.paid_at AS moment FROM orders o
  WHERE o.paid_at IS NOT NULL AND o.paid_minor > 0
  UNION ALL
  SELECT lp.user_id, lp.paid_at FROM legacy_payments lp
), firsts AS (
  SELECT payments.user_id, min(payments.moment) AS first_paid
  FROM payments GROUP BY payments.user_id
)
SELECT
  count(*)::bigint AS payers,
  count(*) FILTER (WHERE usage.first_connected_at IS NOT NULL)::bigint AS connected,
  count(*) FILTER (WHERE usage.first_connected_at IS NOT NULL
    AND usage.first_connected_at <= firsts.first_paid + interval '1 day')::bigint AS connected_within_day
FROM firsts
LEFT JOIN usage ON usage.user_id = firsts.user_id
WHERE firsts.first_paid >= sqlc.arg(since)::timestamptz AND firsts.first_paid < sqlc.arg(until)::timestamptz;

-- name: PayingBaseUsage :one
-- The customers holding a paid or imported period now, and how they use it:
-- online in the last week or month, never connected, and how close they are
-- to their device limit. A customer is online when any Remnawave user they
-- hold was.
WITH paying AS (
  SELECT DISTINCT e.user_id
  FROM entitlements e
  JOIN orders o ON o.id = e.order_id
  LEFT JOIN trial_claims tc ON tc.order_id = o.id
  WHERE e.starts_at <= now() AND e.ends_at > now()
    AND e.status NOT IN ('pending', 'failed')
    AND o.paid_at IS NOT NULL AND o.state <> 'refunded'
    AND tc.order_id IS NULL
    AND (o.subtotal_minor - o.discount_minor > 0 OR o.idempotency_key LIKE 'bedolaga:%')
), usage AS (
  SELECT COALESCE(s.user_id, ru.user_id) AS user_id,
    min(v.first_connected_at) AS first_connected_at,
    max(v.online_at) AS online_at,
    bool_or(v.device_limit > 0 AND v.active_devices >= v.device_limit) AS at_limit,
    bool_or(v.device_limit > 0 AND v.active_devices > v.device_limit) AS over_limit,
    count(*) AS observed
  FROM vpn_usage_observations v
  LEFT JOIN subscriptions s ON s.remnawave_user_id = v.remnawave_user_id
  LEFT JOIN remnawave_users ru ON ru.remnawave_id = v.remnawave_user_id
  GROUP BY 1
)
SELECT
  count(*)::bigint AS customers,
  count(*) FILTER (WHERE usage.observed > 0)::bigint AS observed,
  count(*) FILTER (WHERE usage.online_at >= now() - interval '7 days')::bigint AS online_week,
  count(*) FILTER (WHERE usage.online_at >= now() - interval '30 days')::bigint AS online_month,
  count(*) FILTER (WHERE usage.observed > 0 AND usage.first_connected_at IS NULL)::bigint AS never_connected,
  count(*) FILTER (WHERE usage.at_limit)::bigint AS at_device_limit,
  count(*) FILTER (WHERE usage.over_limit)::bigint AS over_device_limit
FROM paying
LEFT JOIN usage ON usage.user_id = paying.user_id;

-- name: NodeQuality :many
-- Paying customers by the node they last connected through, and how the paid
-- periods that ended in the window fared for the customers of each node, with
-- the support tickets those customers opened in the window. A node is where a
-- customer was last seen, not where they always are; the figure is a pointer
-- to a node worth a look, not a verdict on it.
WITH node_of AS (
  SELECT COALESCE(s.user_id, ru.user_id) AS user_id,
    (array_agg(v.last_node_uuid ORDER BY v.online_at DESC NULLS LAST))[1] AS node_uuid
  FROM vpn_usage_observations v
  LEFT JOIN subscriptions s ON s.remnawave_user_id = v.remnawave_user_id
  LEFT JOIN remnawave_users ru ON ru.remnawave_id = v.remnawave_user_id
  WHERE v.last_node_uuid IS NOT NULL
  GROUP BY 1
), paying AS (
  SELECT DISTINCT e.user_id
  FROM entitlements e
  JOIN orders o ON o.id = e.order_id
  LEFT JOIN trial_claims tc ON tc.order_id = o.id
  WHERE e.starts_at <= now() AND e.ends_at > now()
    AND e.status NOT IN ('pending', 'failed')
    AND o.paid_at IS NOT NULL AND o.state <> 'refunded'
    AND tc.order_id IS NULL
    AND (o.subtotal_minor - o.discount_minor > 0 OR o.idempotency_key LIKE 'bedolaga:%')
), ended AS (
  SELECT e.id, e.user_id, e.subscription_id, e.starts_at, e.ends_at
  FROM entitlements e
  JOIN orders o ON o.id = e.order_id
  LEFT JOIN trial_claims tc ON tc.order_id = o.id
  WHERE e.ends_at >= sqlc.arg(since)::timestamptz AND e.ends_at < sqlc.arg(until)::timestamptz
    AND e.ends_at <= now() - make_interval(secs => sqlc.arg(grace_seconds)::double precision)
    AND e.status NOT IN ('pending', 'failed')
    AND o.paid_at IS NOT NULL AND o.state <> 'refunded'
    AND tc.order_id IS NULL
    AND (o.subtotal_minor - o.discount_minor > 0 OR o.idempotency_key LIKE 'bedolaga:%')
), decided AS (
  SELECT ended.user_id, EXISTS (
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
), tickets AS (
  SELECT t.user_id, count(*) AS opened
  FROM support_tickets t
  WHERE t.created_at >= sqlc.arg(since)::timestamptz AND t.created_at < sqlc.arg(until)::timestamptz
  GROUP BY t.user_id
)
SELECT node_of.node_uuid::text AS node_uuid,
  count(DISTINCT paying.user_id)::bigint AS paying_customers,
  COALESCE((SELECT count(*) FROM decided JOIN node_of n2 ON n2.user_id = decided.user_id
    WHERE n2.node_uuid = node_of.node_uuid), 0)::bigint AS periods_ended,
  COALESCE((SELECT count(*) FROM decided JOIN node_of n2 ON n2.user_id = decided.user_id
    WHERE n2.node_uuid = node_of.node_uuid AND decided.renewed), 0)::bigint AS periods_renewed,
  COALESCE(sum(tickets.opened) FILTER (WHERE paying.user_id IS NOT NULL), 0)::bigint AS tickets
FROM node_of
LEFT JOIN paying ON paying.user_id = node_of.user_id
LEFT JOIN tickets ON tickets.user_id = node_of.user_id
GROUP BY node_of.node_uuid
ORDER BY paying_customers DESC, node_uuid;

-- name: LatestNodeNames :many
-- The name and country each node had in its latest traffic sample.
SELECT DISTINCT ON (s.node_uuid) s.node_uuid, s.node_name, s.country_code
FROM node_traffic_samples s
ORDER BY s.node_uuid, s.sampled_at DESC;
