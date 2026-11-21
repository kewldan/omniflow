-- What a customer merge needs that the schema did not allow.
--
-- A merge ends every session of the account being absorbed, and says so: a
-- session that ended because its account became somebody else's is not a
-- logout and not a suspension, and the security screen reads the reason.
ALTER TABLE customer_sessions
  DROP CONSTRAINT customer_sessions_revoked_reason_known,
  ADD CONSTRAINT customer_sessions_revoked_reason_known CHECK (
    revoked_reason IS NULL OR revoked_reason = ANY (ARRAY[
      'logout', 'logout_all', 'session_revoked', 'identity_unlinked',
      'provider_disabled', 'account_suspended', 'account_deleted', 'expired',
      'reauthenticated', 'account_merged'
    ])
  );

-- The absorbed account's own invite code keeps crediting the person, now the
-- surviving account, the way a code carried from the previous bot does. It is
-- kept as an alias rather than rewritten, because the survivor already has a
-- code of its own and a customer has exactly one.
ALTER TABLE referral_code_aliases
  DROP CONSTRAINT referral_code_aliases_source_known,
  ADD CONSTRAINT referral_code_aliases_source_known CHECK (source = ANY (ARRAY['bedolaga', 'merge']));
