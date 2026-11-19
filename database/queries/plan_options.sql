-- Plan authoring and the subscription options built on it: the squads a
-- customer may choose, and what a plan version offers beside its price.

-- name: InsertPlanVersionSquad :exec
-- One squad a customer may choose on a plan version. The version is immutable
-- once published, so the offered set is written with it and never edited.
INSERT INTO plan_version_squads (plan_version_id, squad_id, label_ru, label_en, sort_order)
VALUES (sqlc.arg(plan_version_id), sqlc.arg(squad_id), sqlc.arg(label_ru), sqlc.arg(label_en), sqlc.arg(sort_order));

-- name: ListPlanVersionAddonIDs :many
SELECT addon_id FROM plan_version_addons WHERE plan_version_id = $1 ORDER BY addon_id;
