-- A customer who signs in again from a browser that already holds their own
-- session ends that session with the reason `reauthenticated` (see "Signing in
-- again" in docs/customer/web-panel.mdx). The reason was introduced in code
-- without widening the column check, so every supersede failed the constraint,
-- was logged and swallowed, and left the old session live beside the new one.
ALTER TABLE customer_sessions
  DROP CONSTRAINT customer_sessions_revoked_reason_check;

ALTER TABLE customer_sessions
  ADD CONSTRAINT customer_sessions_revoked_reason_known CHECK (
    revoked_reason IS NULL
    OR revoked_reason IN (
      'logout', 'logout_all', 'session_revoked', 'identity_unlinked',
      'provider_disabled', 'account_suspended', 'account_deleted', 'expired',
      'reauthenticated'
    )
  );
