-- Operator second-factor hardening.
--
-- A six-digit TOTP code was guarded only by a per-address rate limit, which a
-- caller rotating addresses walks straight past, and an accepted code could be
-- used again for the rest of its validity window. Three things change:
--
-- 1. A half-authenticated session counts its own wrong codes and is revoked on
--    the fifth, so one password sign-in buys a bounded number of guesses.
-- 2. The account counts wrong codes across every session, and that counter is
--    not reset by a correct password — only by a correct second factor — so
--    signing in again with the password does not buy a fresh allowance. Past
--    the lockout threshold the account locks with the same backoff a password
--    failure uses.
-- 3. The account remembers the TOTP time step it last accepted and refuses
--    that step or an earlier one, so a code seen over a shoulder or in a proxy
--    log cannot be replayed within its window.

ALTER TABLE admin_sessions
  ADD COLUMN challenge_failures integer NOT NULL DEFAULT 0,
  ADD CONSTRAINT admin_sessions_challenge_failures_nonnegative CHECK (challenge_failures >= 0);

-- The inline check PostgreSQL named for this column is replaced with an explicit
-- one that also admits the new reason.
ALTER TABLE admin_sessions
  DROP CONSTRAINT admin_sessions_revoked_reason_check;

ALTER TABLE admin_sessions
  ADD CONSTRAINT admin_sessions_revoked_reason_known CHECK (
    revoked_reason IS NULL
    OR revoked_reason IN (
      'logout', 'logout_all', 'password_change', 'admin_revoked', 'expired',
      'challenge_failed'
    )
  );

ALTER TABLE admin_users
  ADD COLUMN totp_failed_count integer NOT NULL DEFAULT 0,
  ADD COLUMN totp_last_used_step bigint,
  ADD CONSTRAINT admin_users_totp_failed_count_nonnegative CHECK (totp_failed_count >= 0),
  ADD CONSTRAINT admin_users_totp_last_used_step_nonnegative
    CHECK (totp_last_used_step IS NULL OR totp_last_used_step >= 0);
