-- Referral codes a customer had in the previous bot. Links already handed out —
-- `t.me/<bot>?start=refAbCd1234`, `<cabinet>/?ref=refAbCd1234` — keep
-- attributing to the same inviter beside the Omniflow code they have now.
--
-- A code is stored upper-cased, the way every referral lookup normalises what
-- a customer typed or a link carried. The previous bot's codes are `ref` and
-- eight letters or digits, or `ref` and six digits, so they never collide with
-- the ten-character Omniflow codes.
CREATE TABLE referral_code_aliases (
  code text PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  source text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT referral_code_aliases_code_shape CHECK (code ~ '^[A-Z0-9_-]{3,32}$'),
  CONSTRAINT referral_code_aliases_source_known CHECK (source IN ('bedolaga'))
);

CREATE INDEX referral_code_aliases_user_idx ON referral_code_aliases (user_id);
