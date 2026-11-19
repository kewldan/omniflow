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
