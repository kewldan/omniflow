-- Plan authoring and the subscription options built on it: the squads a
-- customer may choose, and what a plan version offers beside its price.

-- name: InsertPlanVersionSquad :exec
-- One squad a customer may choose on a plan version. The version is immutable
-- once published, so the offered set is written with it and never edited.
INSERT INTO plan_version_squads (plan_version_id, squad_id, label_ru, label_en, sort_order)
VALUES (sqlc.arg(plan_version_id), sqlc.arg(squad_id), sqlc.arg(label_ru), sqlc.arg(label_en), sqlc.arg(sort_order));

-- name: ListPlanVersionAddonIDs :many
SELECT addon_id FROM plan_version_addons WHERE plan_version_id = $1 ORDER BY addon_id;

-- name: GetSubscriptionPolicy :one
-- The migration seeds the row, so it is always there.
SELECT * FROM subscription_policy WHERE singleton;

-- name: UpdateSubscriptionPolicy :one
UPDATE subscription_policy
SET upgrades_enabled = sqlc.arg(upgrades_enabled),
    downgrades_enabled = sqlc.arg(downgrades_enabled),
    reset_traffic_on_renewal = sqlc.arg(reset_traffic_on_renewal),
    reset_traffic_on_plan_change = sqlc.arg(reset_traffic_on_plan_change),
    reset_devices_on_renewal = sqlc.arg(reset_devices_on_renewal),
    trial_remainder = sqlc.arg(trial_remainder),
    updated_at = now(),
    updated_by = sqlc.narg(updated_by)
WHERE singleton
RETURNING *;

-- name: GetHeldPlanKind :one
-- The kind of plan behind a subscription's current entitlement, for the rule
-- about what a trial's remaining time does on a move to a paid plan.
SELECT p.kind
FROM entitlements e
JOIN plan_versions v ON v.id = e.plan_version_id
JOIN plans p ON p.id = v.plan_id
WHERE e.subscription_id = sqlc.arg(subscription_id) AND e.status <> 'superseded'
ORDER BY (e.starts_at <= now()) DESC, e.ends_at DESC
LIMIT 1;

-- name: GetProvisioningProfile :one
-- What the worker pushes to Remnawave beside the expiry and the limits, for
-- the plan version an entitlement was bought at.
SELECT v.traffic_reset_strategy, v.external_squad_id, p.kind, p.remnawave_tag,
       sp.trial_tag, sp.paid_tag
FROM plan_versions v
JOIN plans p ON p.id = v.plan_id
LEFT JOIN subscription_policy sp ON sp.singleton
WHERE v.id = sqlc.arg(plan_version_id);

-- name: SetPlanVersionRemnawaveProfile :exec
-- Written once, in the transaction that publishes the version.
UPDATE plan_versions
SET traffic_reset_strategy = sqlc.arg(traffic_reset_strategy),
    external_squad_id = sqlc.narg(external_squad_id)
WHERE id = sqlc.arg(plan_version_id);

-- name: SetPlanRemnawaveTag :one
UPDATE plans SET remnawave_tag = sqlc.narg(remnawave_tag)
WHERE id = sqlc.arg(plan_id)
RETURNING *;

-- name: SetPolicyTags :one
UPDATE subscription_policy
SET trial_tag = sqlc.narg(trial_tag), paid_tag = sqlc.narg(paid_tag), updated_at = now()
WHERE singleton
RETURNING *;

-- name: SetTrialPolicy :one
UPDATE subscription_policy
SET trial_disabled_methods = sqlc.arg(trial_disabled_methods)::text[],
    trial_warning_hours = sqlc.arg(trial_warning_hours),
    updated_at = now()
WHERE singleton
RETURNING *;

-- name: SetMaxDeviceLimit :one
UPDATE subscription_policy SET max_device_limit = sqlc.narg(max_device_limit), updated_at = now()
WHERE singleton
RETURNING *;

-- name: SetPlanVersionAddonTrafficCap :exec
UPDATE plan_versions SET max_addon_traffic_bytes = sqlc.narg(max_addon_traffic_bytes)
WHERE id = sqlc.arg(plan_version_id);

-- name: SetAddonVersionLifetime :exec
UPDATE addon_versions SET lifetime_days = sqlc.narg(lifetime_days)
WHERE id = sqlc.arg(addon_version_id);

-- name: GetAddonLimits :one
-- What bounds an add-on purchase against one entitlement: the version's cap
-- on bought traffic, what is already bought and still counting, and the
-- installation's device ceiling.
SELECT v.max_addon_traffic_bytes,
       COALESCE((SELECT sum(ea.traffic_bytes) FROM entitlement_addons ea
                 WHERE ea.entitlement_id = e.id AND ea.expired_at IS NULL), 0)::bigint AS bought_traffic_bytes,
       (SELECT sp.max_device_limit FROM subscription_policy sp WHERE sp.singleton) AS max_device_limit
FROM entitlements e
JOIN plan_versions v ON v.id = e.plan_version_id
WHERE e.id = sqlc.arg(entitlement_id);

-- name: StartAddonLifetime :exec
-- A pack with a lifetime stops counting that many days after it is applied.
UPDATE entitlement_addons ea
SET expires_at = ea.applied_at + make_interval(days => av.lifetime_days)
FROM addon_versions av
WHERE ea.id = sqlc.arg(entitlement_addon_id) AND av.id = ea.addon_version_id
  AND av.lifetime_days IS NOT NULL AND ea.traffic_bytes IS NOT NULL;

-- name: ListDueTrafficPacks :many
SELECT ea.id FROM entitlement_addons ea
JOIN entitlements e ON e.id = ea.entitlement_id
WHERE ea.expires_at <= now() AND ea.expired_at IS NULL
  AND e.status NOT IN ('superseded', 'expired', 'failed')
ORDER BY ea.expires_at
LIMIT sqlc.arg(row_limit);

-- name: ExpireTrafficPack :one
-- Marks one pack as taken back. The predicate makes a replayed pass match
-- nothing, so the traffic is subtracted once.
UPDATE entitlement_addons SET expired_at = now()
WHERE id = sqlc.arg(entitlement_addon_id) AND expired_at IS NULL
RETURNING *;

-- name: SubtractEntitlementTraffic :one
UPDATE entitlements
SET traffic_allowance_bytes = CASE WHEN traffic_allowance_bytes IS NULL THEN NULL
      ELSE greatest(traffic_allowance_bytes - sqlc.arg(traffic_bytes)::bigint, 0) END,
    updated_at = now()
WHERE id = sqlc.arg(entitlement_id)
RETURNING *;

-- name: SetRemnawaveCloseMode :one
UPDATE subscription_policy SET remnawave_close_mode = sqlc.arg(remnawave_close_mode), updated_at = now()
WHERE singleton
RETURNING *;

-- name: CountOpenSubscriptionOrders :one
-- Orders still waiting for money against a subscription. Closing it under
-- them would leave a payment with nowhere to land.
SELECT count(*)::integer FROM orders
WHERE subscription_id = sqlc.arg(subscription_id) AND state IN ('draft', 'pending');

-- name: RetireSubscriptionEntitlements :many
-- Retires every entitlement of a subscription being closed. A paused one
-- leaves its pause with it, which the pairing constraint requires.
UPDATE entitlements
SET status = 'superseded', paused_at = NULL, updated_at = now()
WHERE subscription_id = sqlc.arg(subscription_id) AND status <> 'superseded'
RETURNING *;

-- name: ApplyPlanSquadsToEntitlements :many
-- Moves every live entitlement of a plan onto its newest live version's
-- squads. Only the plan's own squads are swapped: what the customer chose at
-- checkout and what squad add-ons granted — the entitlement's squads beyond
-- its own version's set — are kept.
WITH latest AS (
  SELECT pv.remnawave_squad_ids FROM plan_versions pv
  WHERE pv.plan_id = sqlc.arg(plan_id) AND pv.retired_at IS NULL
  ORDER BY pv.version DESC LIMIT 1
)
UPDATE entitlements e
SET remnawave_squad_ids = (
      SELECT COALESCE(array_agg(DISTINCT squad ORDER BY squad), '{}')
      FROM unnest((SELECT remnawave_squad_ids FROM latest) || ARRAY(
        SELECT unnest(e.remnawave_squad_ids) EXCEPT SELECT unnest(v.remnawave_squad_ids)
      )) AS squad
    ),
    updated_at = now()
FROM plan_versions v
WHERE v.id = e.plan_version_id AND v.plan_id = sqlc.arg(plan_id)
  AND EXISTS (SELECT 1 FROM latest)
  AND e.status IN ('pending', 'active', 'limited', 'paused')
  AND (e.status = 'paused' OR e.ends_at > now())
RETURNING e.*;
