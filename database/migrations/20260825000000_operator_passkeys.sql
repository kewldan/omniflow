-- Passkeys for operators.
--
-- A passkey signs in on its own: the authenticator proves possession of a
-- private key and verifies the person holding it, so an assertion carries both
-- factors and issues a complete session rather than a pending one. The password
-- and its second factor stay as they are, and remain the way back in when every
-- key is lost — which is why this table can be emptied without locking anybody
-- out, and why losing a phone is not an account recovery event.
--
-- Nothing secret is stored here. A passkey's private half never leaves the
-- authenticator; the column below holds the public key, which is useless to
-- anybody who reads this table.
CREATE TABLE admin_passkeys (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  admin_user_id uuid NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,

  -- The credential's own identifier, as the authenticator minted it. It is the
  -- lookup key during an assertion, and it is unique across the installation
  -- rather than per operator: the same credential must not be claimable by two
  -- accounts.
  credential_id bytea NOT NULL UNIQUE,
  public_key bytea NOT NULL,

  -- What the operator calls it. A person with three keys needs to know which
  -- one to revoke when they lose the middle one.
  label text NOT NULL CHECK (char_length(label) BETWEEN 1 AND 60),

  -- The authenticator's own signature counter, as last seen.
  --
  -- It exists to detect a cloned authenticator: a counter that goes backwards
  -- means two devices are answering for one credential. Some authenticators
  -- never implement it and always report zero, which is legal and is why a zero
  -- counter is not treated as a failure.
  sign_count bigint NOT NULL DEFAULT 0 CHECK (sign_count >= 0),

  -- The AAGUID identifies the authenticator model. It is recorded so an
  -- operator can tell one key from another when the labels stop being enough,
  -- and so a fleet running a withdrawn model is findable.
  aaguid bytea,
  -- Whether the credential is discoverable. Only a discoverable one can sign in
  -- without the operator first naming their account, which is the whole point
  -- of the passwordless path.
  discoverable boolean NOT NULL DEFAULT true,
  -- Whether the authenticator verified the person, rather than merely being
  -- present. An assertion without it is not a second factor.
  user_verified boolean NOT NULL DEFAULT true,

  -- Provenance and use, so a stale key is visible as stale.
  created_at timestamptz NOT NULL DEFAULT now(),
  created_ip inet,
  last_used_at timestamptz,
  last_used_ip inet
);

CREATE INDEX admin_passkeys_owner_idx ON admin_passkeys (admin_user_id, created_at DESC);

COMMENT ON COLUMN admin_passkeys.public_key IS
  'The credential public key. The private half never leaves the authenticator, so this column holds nothing an attacker can use.';
COMMENT ON COLUMN admin_passkeys.sign_count IS
  'Last signature counter seen. A decrease indicates a cloned authenticator; a permanent zero means the authenticator does not implement one.';

-- Sessions record how they were established, and a passkey is a method of its
-- own: "signed in with a passkey" and "signed in with a password" are different
-- facts, and an audit that collapsed them could not answer which.
ALTER TABLE admin_sessions
  DROP CONSTRAINT IF EXISTS admin_sessions_auth_methods_known;

ALTER TABLE admin_sessions
  ADD CONSTRAINT admin_sessions_auth_methods_known CHECK (
    auth_methods <@ ARRAY['password', 'totp', 'recovery_code', 'oidc', 'passkey']::text[]
  );
