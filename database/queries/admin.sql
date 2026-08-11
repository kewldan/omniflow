-- Operator identity, sessions, and the audit trail for the v0.6 admin panel.

-- ---------------------------------------------------------------------------
-- Administrator accounts
-- ---------------------------------------------------------------------------

-- name: CountAdminUsers :one
SELECT count(*)::bigint FROM admin_users;

-- name: CreateAdminUser :one
INSERT INTO admin_users (
  email, email_normalized, display_name, password_hash, locale, timezone, password_changed_at
) VALUES (
  sqlc.arg(email), sqlc.arg(email_normalized), sqlc.arg(display_name),
  sqlc.narg(password_hash), sqlc.arg(locale), sqlc.arg(timezone), sqlc.narg(password_changed_at)
)
ON CONFLICT (email_normalized) DO NOTHING
RETURNING *;

-- name: GetAdminUser :one
SELECT * FROM admin_users WHERE id = $1;

-- name: GetAdminUserByEmail :one
SELECT * FROM admin_users WHERE email_normalized = $1;

-- name: ListAdminUsers :many
-- Keyset pagination on (created_at, id): the pair is unique, so a page boundary
-- never repeats or skips a row even when several accounts share a timestamp.
SELECT * FROM admin_users
WHERE (
    sqlc.narg(cursor_created_at)::timestamptz IS NULL
    OR (created_at, id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_size);

-- name: UpdateAdminUserProfile :one
UPDATE admin_users
SET display_name = sqlc.arg(display_name),
    locale = sqlc.arg(locale),
    timezone = sqlc.arg(timezone),
    updated_at = now()
WHERE id = sqlc.arg(admin_user_id)
RETURNING *;

-- name: SetAdminUserPassword :one
UPDATE admin_users
SET password_hash = sqlc.arg(password_hash),
    password_changed_at = now(),
    failed_login_count = 0,
    locked_until = NULL,
    updated_at = now()
WHERE id = sqlc.arg(admin_user_id)
RETURNING *;

-- name: SetAdminUserStatus :one
UPDATE admin_users
SET status = sqlc.arg(status),
    disabled_at = CASE WHEN sqlc.arg(status) = 'active' THEN NULL ELSE now() END,
    updated_at = now()
WHERE id = sqlc.arg(admin_user_id)
RETURNING *;

-- name: RecordAdminLoginSuccess :one
UPDATE admin_users
SET failed_login_count = 0, locked_until = NULL, last_login_at = now(), updated_at = now()
WHERE id = sqlc.arg(admin_user_id)
RETURNING *;

-- name: RecordAdminLoginFailure :one
-- The caller computes the lockout deadline from the resulting attempt count, so
-- the backoff curve stays in Go where it is unit-testable.
UPDATE admin_users
SET failed_login_count = failed_login_count + 1,
    locked_until = sqlc.narg(locked_until),
    updated_at = now()
WHERE id = sqlc.arg(admin_user_id)
RETURNING *;

-- name: SetAdminTOTPSecret :one
-- Starts enrolment. `totp_confirmed_at` is deliberately cleared: a secret that
-- has not been proven by a code cannot satisfy a login challenge.
UPDATE admin_users
SET totp_secret_ciphertext = sqlc.arg(totp_secret_ciphertext),
    totp_confirmed_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg(admin_user_id)
RETURNING *;

-- name: ConfirmAdminTOTP :one
UPDATE admin_users
SET totp_confirmed_at = now(), updated_at = now()
WHERE id = sqlc.arg(admin_user_id) AND totp_secret_ciphertext IS NOT NULL
RETURNING *;

-- name: DisableAdminTOTP :one
UPDATE admin_users
SET totp_secret_ciphertext = NULL, totp_confirmed_at = NULL, updated_at = now()
WHERE id = sqlc.arg(admin_user_id)
RETURNING *;

-- ---------------------------------------------------------------------------
-- Role grants
-- ---------------------------------------------------------------------------

-- name: GrantAdminRole :exec
INSERT INTO admin_user_roles (admin_user_id, role, granted_by)
VALUES (sqlc.arg(admin_user_id), sqlc.arg(role), sqlc.narg(granted_by))
ON CONFLICT (admin_user_id, role) DO NOTHING;

-- name: RevokeAdminRole :exec
DELETE FROM admin_user_roles
WHERE admin_user_id = sqlc.arg(admin_user_id) AND role = sqlc.arg(role);

-- name: ListAdminRoles :many
SELECT role FROM admin_user_roles WHERE admin_user_id = $1 ORDER BY role;

-- name: ListAdminRolesForUsers :many
SELECT admin_user_id, role FROM admin_user_roles
WHERE admin_user_id = ANY(sqlc.arg(admin_user_ids)::uuid[])
ORDER BY admin_user_id, role;

-- name: CountAdminOwners :one
-- Guards the "an installation always keeps one owner" invariant. Only accounts
-- that could actually sign in count, so suspending the last owner is refused
-- for the same reason revoking the role is.
SELECT count(*)::bigint
FROM admin_user_roles r
JOIN admin_users u ON u.id = r.admin_user_id
WHERE r.role = 'owner' AND u.status = 'active' AND u.id <> sqlc.arg(excluding_admin_user_id);

-- ---------------------------------------------------------------------------
-- Sessions
-- ---------------------------------------------------------------------------

-- name: CreateAdminSession :one
INSERT INTO admin_sessions (
  admin_user_id, token_hash, csrf_secret, pending_totp, auth_methods,
  ip, user_agent, idle_expires_at, absolute_expires_at
) VALUES (
  sqlc.arg(admin_user_id), sqlc.arg(token_hash), sqlc.arg(csrf_secret),
  sqlc.arg(pending_totp), sqlc.arg(auth_methods),
  sqlc.narg(ip), sqlc.narg(user_agent),
  sqlc.arg(idle_expires_at), sqlc.arg(absolute_expires_at)
)
RETURNING *;

-- name: GetAdminSessionByToken :one
-- Returns the session and its owner in one round trip. Liveness is decided in
-- Go so an expired session can be distinguished from a missing one and revoked
-- explicitly, rather than silently disappearing from the result set.
SELECT sqlc.embed(s), sqlc.embed(u)
FROM admin_sessions s
JOIN admin_users u ON u.id = s.admin_user_id
WHERE s.token_hash = $1;

-- name: TouchAdminSession :one
-- Slides the inactivity window forward. The absolute deadline is never
-- extended, so continuous activity cannot keep a session alive indefinitely.
UPDATE admin_sessions
SET last_seen_at = now(), idle_expires_at = sqlc.arg(idle_expires_at)
WHERE id = sqlc.arg(session_id) AND revoked_at IS NULL
RETURNING *;

-- name: RotateAdminSessionToken :one
UPDATE admin_sessions
SET token_hash = sqlc.arg(token_hash),
    rotated_at = now(),
    last_seen_at = now(),
    idle_expires_at = sqlc.arg(idle_expires_at)
WHERE id = sqlc.arg(session_id) AND revoked_at IS NULL
RETURNING *;

-- name: CompleteAdminSessionChallenge :one
-- Promotes a half-authenticated session once the second factor is proven. The
-- token is rotated in the same statement so the cookie that existed during the
-- challenge cannot be replayed as a fully authenticated one.
UPDATE admin_sessions
SET pending_totp = false,
    auth_methods = sqlc.arg(auth_methods),
    token_hash = sqlc.arg(token_hash),
    rotated_at = now(),
    last_seen_at = now(),
    idle_expires_at = sqlc.arg(idle_expires_at)
WHERE id = sqlc.arg(session_id) AND revoked_at IS NULL AND pending_totp = true
RETURNING *;

-- name: RevokeAdminSession :one
UPDATE admin_sessions
SET revoked_at = now(), revoked_reason = sqlc.arg(revoked_reason)
WHERE id = sqlc.arg(session_id) AND revoked_at IS NULL
RETURNING *;

-- name: RevokeAdminSessionsForUser :execrows
-- Logout-everywhere. `keep_session_id` lets the caller preserve the session
-- that requested it, which is what a password change should do.
UPDATE admin_sessions
SET revoked_at = now(), revoked_reason = sqlc.arg(revoked_reason)
WHERE admin_user_id = sqlc.arg(admin_user_id)
  AND revoked_at IS NULL
  AND (sqlc.narg(keep_session_id)::uuid IS NULL OR id <> sqlc.narg(keep_session_id)::uuid);

-- name: ListAdminSessions :many
SELECT * FROM admin_sessions
WHERE admin_user_id = $1 AND revoked_at IS NULL AND absolute_expires_at > now()
ORDER BY last_seen_at DESC
LIMIT sqlc.arg(page_size);

-- name: PurgeExpiredAdminSessions :execrows
DELETE FROM admin_sessions
WHERE absolute_expires_at < sqlc.arg(cutoff)
   OR (revoked_at IS NOT NULL AND revoked_at < sqlc.arg(cutoff));

-- ---------------------------------------------------------------------------
-- Recovery codes
-- ---------------------------------------------------------------------------

-- name: DeleteAdminRecoveryCodes :exec
DELETE FROM admin_recovery_codes WHERE admin_user_id = $1;

-- name: InsertAdminRecoveryCode :exec
INSERT INTO admin_recovery_codes (admin_user_id, code_hash)
VALUES (sqlc.arg(admin_user_id), sqlc.arg(code_hash))
ON CONFLICT (admin_user_id, code_hash) DO NOTHING;

-- name: ConsumeAdminRecoveryCode :one
-- Single-use by construction: the `used_at IS NULL` predicate means a replay of
-- the same code updates no row and returns no result.
UPDATE admin_recovery_codes
SET used_at = now()
WHERE admin_user_id = sqlc.arg(admin_user_id)
  AND code_hash = sqlc.arg(code_hash)
  AND used_at IS NULL
RETURNING *;

-- name: CountUnusedAdminRecoveryCodes :one
SELECT count(*)::bigint FROM admin_recovery_codes
WHERE admin_user_id = $1 AND used_at IS NULL;

-- ---------------------------------------------------------------------------
-- Password resets
-- ---------------------------------------------------------------------------

-- name: CreateAdminPasswordReset :one
INSERT INTO admin_password_resets (admin_user_id, token_hash, requested_ip, expires_at)
VALUES (sqlc.arg(admin_user_id), sqlc.arg(token_hash), sqlc.narg(requested_ip), sqlc.arg(expires_at))
RETURNING *;

-- name: GetAdminPasswordReset :one
SELECT sqlc.embed(r), sqlc.embed(u)
FROM admin_password_resets r
JOIN admin_users u ON u.id = r.admin_user_id
WHERE r.token_hash = $1;

-- name: ConsumeAdminPasswordReset :one
UPDATE admin_password_resets
SET used_at = now()
WHERE id = sqlc.arg(reset_id) AND used_at IS NULL AND expires_at > now()
RETURNING *;

-- name: InvalidateAdminPasswordResets :execrows
UPDATE admin_password_resets
SET used_at = now()
WHERE admin_user_id = sqlc.arg(admin_user_id) AND used_at IS NULL;

-- ---------------------------------------------------------------------------
-- First-owner bootstrap
-- ---------------------------------------------------------------------------

-- name: CreateAdminSetupToken :one
INSERT INTO admin_setup_tokens (token_hash, expires_at)
VALUES (sqlc.arg(token_hash), sqlc.arg(expires_at))
RETURNING *;

-- name: GetAdminSetupToken :one
SELECT * FROM admin_setup_tokens WHERE token_hash = $1;

-- name: ConsumeAdminSetupToken :one
UPDATE admin_setup_tokens
SET consumed_at = now(), consumed_by = sqlc.arg(consumed_by)
WHERE id = sqlc.arg(setup_token_id) AND consumed_at IS NULL AND expires_at > now()
RETURNING *;

-- name: ExpireAdminSetupTokens :execrows
-- Retires outstanding tokens by moving their expiry into the past rather than
-- marking them consumed: `admin_setup_tokens_consumption_complete` requires a
-- consuming account, and an unredeemed token has none. Both the lookup and the
-- redemption already refuse an expired row.
UPDATE admin_setup_tokens
SET expires_at = now()
WHERE consumed_at IS NULL AND expires_at > now();

-- ---------------------------------------------------------------------------
-- Optional OIDC configuration
-- ---------------------------------------------------------------------------

-- name: ListAdminOIDCProviders :many
SELECT * FROM admin_oidc_providers ORDER BY display_name;

-- name: ListEnabledAdminOIDCProviders :many
SELECT * FROM admin_oidc_providers WHERE enabled = true ORDER BY display_name;

-- name: GetAdminOIDCProviderBySlug :one
SELECT * FROM admin_oidc_providers WHERE slug = $1;

-- name: UpsertAdminOIDCProvider :one
INSERT INTO admin_oidc_providers (
  slug, display_name, issuer, discovery_url, client_id, client_secret_ciphertext,
  scopes, enabled, require_verified_email, allow_auto_provision, auto_provision_role
) VALUES (
  sqlc.arg(slug), sqlc.arg(display_name), sqlc.arg(issuer), sqlc.arg(discovery_url),
  sqlc.arg(client_id), sqlc.narg(client_secret_ciphertext), sqlc.arg(scopes),
  sqlc.arg(enabled), sqlc.arg(require_verified_email),
  sqlc.arg(allow_auto_provision), sqlc.narg(auto_provision_role)
)
ON CONFLICT (slug) DO UPDATE
SET display_name = EXCLUDED.display_name,
    issuer = EXCLUDED.issuer,
    discovery_url = EXCLUDED.discovery_url,
    client_id = EXCLUDED.client_id,
    -- A null secret on update means "leave the stored secret alone", so the
    -- panel can render and re-save the form without ever echoing the secret.
    client_secret_ciphertext = COALESCE(
      EXCLUDED.client_secret_ciphertext, admin_oidc_providers.client_secret_ciphertext
    ),
    scopes = EXCLUDED.scopes,
    enabled = EXCLUDED.enabled,
    require_verified_email = EXCLUDED.require_verified_email,
    allow_auto_provision = EXCLUDED.allow_auto_provision,
    auto_provision_role = EXCLUDED.auto_provision_role,
    updated_at = now()
RETURNING *;

-- name: DeleteAdminOIDCProvider :exec
DELETE FROM admin_oidc_providers WHERE slug = $1;

-- name: GetAdminOIDCIdentity :one
SELECT sqlc.embed(i), sqlc.embed(u)
FROM admin_oidc_identities i
JOIN admin_users u ON u.id = i.admin_user_id
WHERE i.provider_id = sqlc.arg(provider_id) AND i.subject = sqlc.arg(subject);

-- name: LinkAdminOIDCIdentity :one
INSERT INTO admin_oidc_identities (admin_user_id, provider_id, subject)
VALUES (sqlc.arg(admin_user_id), sqlc.arg(provider_id), sqlc.arg(subject))
ON CONFLICT (provider_id, subject) DO NOTHING
RETURNING *;

-- name: RecordAdminOIDCLogin :exec
UPDATE admin_oidc_identities SET last_login_at = now() WHERE id = $1;

-- name: CountAdminOIDCIdentities :one
SELECT count(*)::bigint FROM admin_oidc_identities WHERE admin_user_id = $1;

-- ---------------------------------------------------------------------------
-- Audit trail
-- ---------------------------------------------------------------------------

-- Audit events are written through `InsertAuditEvent` in commerce.sql, which is
-- the single writer for the trail. The queries below only read it.

-- name: SearchAuditEvents :many
-- Keyset pagination on (occurred_at, id) descending. Every filter is optional
-- and null-tolerant, so the panel can compose them from URL state without
-- building SQL on the client side.
SELECT * FROM audit_events
WHERE (
    sqlc.narg(cursor_occurred_at)::timestamptz IS NULL
    OR (occurred_at, id) < (sqlc.narg(cursor_occurred_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
  AND (sqlc.narg(category)::text IS NULL OR category = sqlc.narg(category))
  AND (sqlc.narg(outcome)::text IS NULL OR outcome = sqlc.narg(outcome))
  AND (sqlc.narg(actor_type)::text IS NULL OR actor_type = sqlc.narg(actor_type))
  AND (sqlc.narg(actor_id)::text IS NULL OR actor_id = sqlc.narg(actor_id))
  AND (sqlc.narg(action)::text IS NULL OR action = sqlc.narg(action))
  AND (sqlc.narg(target_type)::text IS NULL OR target_type = sqlc.narg(target_type))
  AND (sqlc.narg(target_id)::text IS NULL OR target_id = sqlc.narg(target_id))
  AND (sqlc.narg(occurred_from)::timestamptz IS NULL OR occurred_at >= sqlc.narg(occurred_from))
  AND (sqlc.narg(occurred_to)::timestamptz IS NULL OR occurred_at < sqlc.narg(occurred_to))
ORDER BY occurred_at DESC, id DESC
LIMIT sqlc.arg(page_size);

-- name: ListAuditEventActions :many
-- Populates the action filter without scanning the whole table from the client.
SELECT DISTINCT action FROM audit_events ORDER BY action;
