-- Customer-facing payment buttons: how each provider method is presented and
-- to whom. See payment_method_settings in the migrations.

-- name: ListPaymentMethodSettings :many
SELECT * FROM payment_method_settings ORDER BY provider, method;

-- name: UpsertPaymentMethodSettings :one
INSERT INTO payment_method_settings (
  provider, method, enabled, display_order, name_ru, name_en, description_ru, description_en,
  min_minor, max_minor, quick_amounts_minor, login_kinds, topup_audience, loyalty_tier_codes,
  open_directly, updated_by
) VALUES (
  sqlc.arg(provider), sqlc.arg(method), sqlc.arg(enabled), sqlc.arg(display_order),
  sqlc.arg(name_ru), sqlc.arg(name_en), sqlc.arg(description_ru), sqlc.arg(description_en),
  sqlc.narg(min_minor), sqlc.narg(max_minor), sqlc.arg(quick_amounts_minor)::bigint[],
  sqlc.arg(login_kinds)::text[], sqlc.arg(topup_audience), sqlc.arg(loyalty_tier_codes)::text[],
  sqlc.arg(open_directly), sqlc.narg(updated_by)
)
ON CONFLICT (provider, method) DO UPDATE
SET enabled = EXCLUDED.enabled,
    display_order = EXCLUDED.display_order,
    name_ru = EXCLUDED.name_ru,
    name_en = EXCLUDED.name_en,
    description_ru = EXCLUDED.description_ru,
    description_en = EXCLUDED.description_en,
    min_minor = EXCLUDED.min_minor,
    max_minor = EXCLUDED.max_minor,
    quick_amounts_minor = EXCLUDED.quick_amounts_minor,
    login_kinds = EXCLUDED.login_kinds,
    topup_audience = EXCLUDED.topup_audience,
    loyalty_tier_codes = EXCLUDED.loyalty_tier_codes,
    open_directly = EXCLUDED.open_directly,
    updated_by = EXCLUDED.updated_by,
    updated_at = now()
RETURNING *;

-- name: CustomerPaymentAudience :one
-- The three facts a payment button's visibility rules read: how the customer
-- signs in, whether a top-up of theirs was ever paid, and their loyalty tier.
SELECT
  COALESCE((
    SELECT array_agg(DISTINCT CASE WHEN i.provider LIKE 'oidc:%' THEN 'oidc' ELSE i.provider END)
    FROM identities i WHERE i.user_id = sqlc.arg(user_id) AND i.status = 'active'
  ), '{}')::text[] AS login_kinds,
  EXISTS (
    SELECT 1 FROM orders o
    WHERE o.user_id = sqlc.arg(user_id) AND o.operation = 'topup' AND o.paid_minor > 0
  ) AS topped_up,
  COALESCE((
    SELECT t.code FROM loyalty_standings s JOIN loyalty_tiers t ON t.id = s.tier_id
    WHERE s.user_id = sqlc.arg(user_id)
    ORDER BY s.updated_at DESC LIMIT 1
  ), '')::text AS tier_code;
