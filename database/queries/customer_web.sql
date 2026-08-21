-- Customer web sign-in, sessions, and account security for v0.9.

-- ---------------------------------------------------------------------------
-- Identity resolution
-- ---------------------------------------------------------------------------

-- name: GetActiveIdentityBySubject :one
-- The single lookup every sign-in route funnels through. Matching on the
-- (provider, subject) pair and nothing else is what makes an upstream email
-- change a non-event: the subject is the identifier, the address is a claim.
SELECT i.*, u.status AS user_status, u.locale AS user_locale, u.timezone AS user_timezone
FROM identities i
JOIN users u ON u.id = i.user_id
WHERE i.provider = sqlc.arg(provider) AND i.provider_subject = sqlc.arg(provider_subject)
  AND i.status = 'active';

-- name: CreateCustomerForSignIn :one
-- Used only when a sign-in route is allowed to provision. The row it creates is
-- an ordinary customer with no entitlement, no balance, and no Remnawave user.
INSERT INTO users (status, locale, timezone)
VALUES ('active', sqlc.arg(locale), sqlc.arg(timezone))
RETURNING *;

-- ---------------------------------------------------------------------------
-- Sessions
-- ---------------------------------------------------------------------------

-- name: CreateCustomerSession :one
INSERT INTO customer_sessions (
  user_id, token_hash, csrf_secret, auth_method, auth_provider,
  ip, user_agent, idle_expires_at, absolute_expires_at
) VALUES (
  sqlc.arg(user_id), sqlc.arg(token_hash), sqlc.arg(csrf_secret),
  sqlc.arg(auth_method), sqlc.narg(auth_provider),
  sqlc.narg(ip), sqlc.narg(user_agent),
  now() + sqlc.arg(idle_window)::interval,
  now() + sqlc.arg(absolute_window)::interval
)
RETURNING *;

-- name: GetCustomerSessionByToken :one
-- Resolves a cookie into a session and the customer behind it in one round trip,
-- because this runs on every authenticated request.
SELECT s.*, u.status AS user_status, u.locale AS user_locale, u.timezone AS user_timezone
FROM customer_sessions s
JOIN users u ON u.id = s.user_id
WHERE s.token_hash = sqlc.arg(token_hash);

-- name: TouchCustomerSession :one
-- Slides the inactivity deadline forward, clamped so it can never pass the
-- absolute one. Without the clamp a request made just before the absolute
-- horizon would extend a session the absolute limit had already ended.
UPDATE customer_sessions
SET last_seen_at = now(),
    idle_expires_at = LEAST(now() + sqlc.arg(idle_window)::interval, absolute_expires_at)
WHERE id = sqlc.arg(session_id) AND revoked_at IS NULL
RETURNING *;

-- name: RotateCustomerSessionToken :one
-- Swapping the token behind a live session shortens the window in which one
-- captured from a log or a proxy stays replayable. The unique index on
-- `token_hash` means a colliding rotation fails rather than merging sessions.
--
-- The swap is a compare-and-set on the current digest. A browser fires several
-- requests at once when a page opens, and when the session is due for rotation
-- every one of them arrives holding the same cookie; without the predicate each
-- would install its own token and the last writer would silently invalidate the
-- cookie the others had already told the browser to keep. With it exactly one
-- request rotates and the rest find zero rows and carry on with the token they
-- came with.
UPDATE customer_sessions
SET token_hash = sqlc.arg(token_hash),
    rotated_at = now(),
    last_seen_at = now(),
    idle_expires_at = LEAST(now() + sqlc.arg(idle_window)::interval, absolute_expires_at)
WHERE id = sqlc.arg(session_id)
  AND token_hash = sqlc.arg(current_token_hash)
  AND revoked_at IS NULL
RETURNING *;

-- name: GetCustomerSessionByID :one
-- The grace path after a rotation: a request that arrived with the superseded
-- cookie resolves the session by the identifier the short-lived forwarding
-- entry names, rather than by a digest the table no longer holds.
SELECT s.*, u.status AS user_status, u.locale AS user_locale, u.timezone AS user_timezone
FROM customer_sessions s
JOIN users u ON u.id = s.user_id
WHERE s.id = sqlc.arg(session_id);

-- name: RevokeCustomerSession :one
-- Guarded by `user_id` as well as `id`, so a customer can only ever end one of
-- their own sessions even if they learn another session's identifier.
UPDATE customer_sessions
SET revoked_at = now(), revoked_reason = sqlc.arg(revoked_reason)
WHERE id = sqlc.arg(session_id) AND user_id = sqlc.arg(user_id) AND revoked_at IS NULL
RETURNING *;

-- name: RevokeCustomerSessionsForUser :many
-- "Sign out everywhere". `except_session_id` keeps the caller's own session
-- alive when the customer asked to end the others rather than all of them.
UPDATE customer_sessions
SET revoked_at = now(), revoked_reason = sqlc.arg(revoked_reason)
WHERE user_id = sqlc.arg(user_id) AND revoked_at IS NULL
  AND (sqlc.narg(except_session_id)::uuid IS NULL OR id <> sqlc.narg(except_session_id)::uuid)
RETURNING id;

-- name: RevokeCustomerSessionsForProvider :many
-- Every session established through one OIDC provider. An operator disabling or
-- removing a provider needs the sessions it minted to end with it; leaving them
-- live would mean the provider is off but its access is not.
UPDATE customer_sessions
SET revoked_at = now(), revoked_reason = 'provider_disabled'
WHERE auth_method = 'oidc' AND auth_provider = sqlc.arg(auth_provider) AND revoked_at IS NULL
RETURNING id;

-- name: ListCustomerSessions :many
-- The customer's own device list, live sessions first and most recent first.
SELECT * FROM customer_sessions
WHERE user_id = sqlc.arg(user_id) AND revoked_at IS NULL
  AND absolute_expires_at > now() AND idle_expires_at > now()
ORDER BY last_seen_at DESC
LIMIT sqlc.arg(page_size);

-- name: DeleteExpiredCustomerSessions :execrows
-- Retention sweep. A session is removed only once it is well past any use, so
-- the security list can still explain a recent sign-out.
DELETE FROM customer_sessions
WHERE absolute_expires_at < now() - sqlc.arg(retention)::interval;

-- ---------------------------------------------------------------------------
-- Magic links
-- ---------------------------------------------------------------------------

-- name: CreateCustomerMagicLink :one
INSERT INTO customer_magic_links (user_id, token_hash, requested_ip, expires_at)
VALUES (
  sqlc.arg(user_id), sqlc.arg(token_hash), sqlc.narg(requested_ip),
  now() + sqlc.arg(lifetime)::interval
)
RETURNING *;

-- name: ConsumeCustomerMagicLink :one
-- Single use is a property of this statement, not of the code around it: the
-- `consumed_at IS NULL` predicate is evaluated under the row lock the UPDATE
-- takes, so two browsers racing on one link produce exactly one winner.
UPDATE customer_magic_links
SET consumed_at = now(), consumed_ip = sqlc.narg(consumed_ip)
WHERE token_hash = sqlc.arg(token_hash)
  AND consumed_at IS NULL
  AND expires_at > now()
RETURNING *;

-- name: CountRecentCustomerMagicLinks :one
-- Feeds the per-customer request limit, so asking for a link repeatedly cannot
-- be used to flood somebody's Telegram chat.
SELECT count(*)::bigint FROM customer_magic_links
WHERE user_id = sqlc.arg(user_id) AND created_at > now() - sqlc.arg(lookback)::interval;

-- name: DeleteExpiredCustomerMagicLinks :execrows
DELETE FROM customer_magic_links WHERE expires_at < now() - sqlc.arg(retention)::interval;

-- ---------------------------------------------------------------------------
-- Security events
-- ---------------------------------------------------------------------------

-- name: InsertCustomerSecurityEvent :one
INSERT INTO customer_security_events (user_id, event, ip, user_agent, request_id, metadata)
VALUES (
  sqlc.arg(user_id), sqlc.arg(event), sqlc.narg(ip),
  sqlc.narg(user_agent), sqlc.narg(request_id), sqlc.arg(metadata)
)
RETURNING *;

-- name: ListCustomerSecurityEvents :many
SELECT * FROM customer_security_events
WHERE user_id = sqlc.arg(user_id)
  AND (
    sqlc.narg(cursor_occurred_at)::timestamptz IS NULL
    OR (occurred_at, id) < (sqlc.narg(cursor_occurred_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
ORDER BY occurred_at DESC, id DESC
LIMIT sqlc.arg(page_size);

-- name: DeleteExpiredCustomerSecurityEvents :execrows
DELETE FROM customer_security_events WHERE occurred_at < now() - sqlc.arg(retention)::interval;

-- ---------------------------------------------------------------------------
-- Customer OIDC providers
-- ---------------------------------------------------------------------------

-- name: ListCustomerOIDCProviders :many
SELECT * FROM customer_oidc_providers ORDER BY sort_order, slug;

-- name: ListEnabledCustomerOIDCProviders :many
SELECT * FROM customer_oidc_providers WHERE enabled ORDER BY sort_order, slug;

-- name: GetCustomerOIDCProvider :one
SELECT * FROM customer_oidc_providers WHERE slug = $1;

-- name: UpsertCustomerOIDCProvider :one
-- The client secret is only overwritten when a new one is supplied, so saving
-- the form without retyping it does not blank the stored credential.
INSERT INTO customer_oidc_providers (
  slug, display_name, issuer, discovery_url, client_id, client_secret_ciphertext,
  scopes, enabled, icon, sort_order, require_verified_email, allow_auto_provision,
  disabled_at
) VALUES (
  sqlc.arg(slug), sqlc.arg(display_name), sqlc.arg(issuer), sqlc.arg(discovery_url),
  sqlc.arg(client_id), sqlc.narg(client_secret_ciphertext), sqlc.arg(scopes),
  sqlc.arg(enabled), sqlc.narg(icon), sqlc.arg(sort_order),
  sqlc.arg(require_verified_email), sqlc.arg(allow_auto_provision),
  CASE WHEN sqlc.arg(enabled)::boolean THEN NULL ELSE now() END
)
ON CONFLICT (slug) DO UPDATE
SET display_name = EXCLUDED.display_name,
    issuer = EXCLUDED.issuer,
    discovery_url = EXCLUDED.discovery_url,
    client_id = EXCLUDED.client_id,
    client_secret_ciphertext = COALESCE(
      EXCLUDED.client_secret_ciphertext, customer_oidc_providers.client_secret_ciphertext
    ),
    scopes = EXCLUDED.scopes,
    enabled = EXCLUDED.enabled,
    icon = EXCLUDED.icon,
    sort_order = EXCLUDED.sort_order,
    require_verified_email = EXCLUDED.require_verified_email,
    allow_auto_provision = EXCLUDED.allow_auto_provision,
    disabled_at = CASE
      WHEN EXCLUDED.enabled THEN NULL
      ELSE COALESCE(customer_oidc_providers.disabled_at, now())
    END,
    updated_at = now()
RETURNING *;

-- name: DeleteCustomerOIDCProvider :execrows
DELETE FROM customer_oidc_providers WHERE slug = $1;

-- ---------------------------------------------------------------------------
-- Linked sign-in methods
-- ---------------------------------------------------------------------------

-- name: ListCustomerSignInIdentities :many
-- Everything that can currently sign this customer in. The unlink guard reads
-- this list rather than counting in SQL, so one rule in `internal/customer`
-- decides what "the last usable method" means.
SELECT * FROM identities
WHERE user_id = sqlc.arg(user_id) AND status = 'active'
  AND (provider = 'telegram' OR provider LIKE 'oidc:%')
ORDER BY created_at;

-- name: GetCustomerIdentityForUnlink :one
SELECT * FROM identities WHERE id = sqlc.arg(identity_id) AND user_id = sqlc.arg(user_id);
