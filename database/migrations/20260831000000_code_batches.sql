-- Wholesale code batches: selling a block of access to a distributor.
--
-- Promo codes belong to a promotion and discount a purchase the customer still
-- makes. Gifts are issued one at a time and are bought through an order. Neither
-- covers the case a reseller arrangement actually is: generate two hundred codes
-- at an agreed price, hand over the list, and be able to revoke whatever is left
-- when the list leaks.
--
-- The codes are the same sixteen Crockford characters a gift uses — see
-- internal/accesscode — so one redemption box accepts either and a customer
-- never has to know which kind of code they were handed.

CREATE TABLE code_batches (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),

  -- What the operator calls this batch. It is the thing they will search for
  -- when the distributor emails about "the codes from March", so it is unique
  -- and required rather than a free-form note.
  reference text NOT NULL UNIQUE CHECK (reference ~ '^[a-zA-Z0-9][a-zA-Z0-9 ._-]{2,79}$'),

  -- What each code grants. A batch sells one thing: mixing plans inside a batch
  -- would make "revoke the remainder" ambiguous about what was revoked.
  plan_version_id uuid NOT NULL REFERENCES plan_versions(id),
  quantity integer NOT NULL CHECK (quantity BETWEEN 1 AND 10000),

  -- The agreed wholesale price per code.
  --
  -- Omniflow does not charge it. The money changed hands outside the product,
  -- which is what wholesale means, and this column exists so the arrangement is
  -- recorded somewhere rather than living in an email. Zero is allowed: a batch
  -- given away for a partnership is a real thing and pretending it had a price
  -- would be worse than recording that it had none.
  unit_price_minor bigint NOT NULL CHECK (unit_price_minor >= 0),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),

  note text CHECK (note IS NULL OR char_length(note) <= 500),

  -- When unredeemed codes stop working. NULL means they do not expire, which is
  -- a decision an operator can make and a liability they then carry.
  expires_at timestamptz,

  -- Revoking the batch revokes every code in it that nobody has redeemed. The
  -- reason is required because this is the action taken when a list leaks, and
  -- "why were these three hundred codes killed" is the question somebody asks
  -- six months later.
  revoked_at timestamptz,
  revoked_by uuid REFERENCES admin_users(id),
  revoke_reason text CHECK (revoke_reason IS NULL OR char_length(revoke_reason) BETWEEN 3 AND 500),
  CONSTRAINT code_batches_revocation_complete CHECK (
    (revoked_at IS NULL) = (revoke_reason IS NULL)
  ),

  created_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid REFERENCES admin_users(id)
);

CREATE TABLE access_codes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  batch_id uuid NOT NULL REFERENCES code_batches(id) ON DELETE CASCADE,

  -- Only the digest is stored, exactly as for a gift. The plaintext exists in
  -- one place — the response to the call that generated the batch — and after
  -- that nowhere at all. A dump of this table yields nothing redeemable, and an
  -- operator who loses the list cannot recover it, which is stated on the screen
  -- rather than discovered.
  code_hash bytea NOT NULL UNIQUE CHECK (octet_length(code_hash) = 32),
  -- The last four characters, so an operator can match a support question to a
  -- row without holding anything redeemable.
  code_hint text NOT NULL CHECK (code_hint ~ '^[0-9A-Z]{4}$'),

  status text NOT NULL DEFAULT 'issued' CHECK (
    status IN ('issued', 'redeemed', 'revoked')
  ),

  redeemed_by uuid REFERENCES users(id),
  redeemed_entitlement_id uuid REFERENCES entitlements(id),
  redeemed_at timestamptz,

  -- Redemption is a fact recorded once. The three columns move together or not
  -- at all, so a row can never claim to have been redeemed by nobody.
  CONSTRAINT access_codes_redemption_complete CHECK (
    (status = 'redeemed') = (redeemed_at IS NOT NULL)
    AND (redeemed_at IS NULL) = (redeemed_by IS NULL)
  ),

  created_at timestamptz NOT NULL DEFAULT now()
);

-- Redemption looks a code up by its digest, which is the only lookup on this
-- table that happens on a customer's request.
CREATE INDEX access_codes_batch_idx ON access_codes (batch_id, status);

COMMENT ON COLUMN access_codes.code_hash IS
  'SHA-256 of the code. The plaintext is returned once, when the batch is generated, and is never recoverable afterwards.';

-- Redeeming a code produces an order of its own, worth nothing.
--
-- `entitlements.order_id` is NOT NULL, and that is a constraint worth keeping:
-- every entitlement this installation has ever granted can be traced to the
-- transaction that produced it. A wholesale redemption has no payment — the
-- distributor paid outside the product, which is what wholesale means — so it
-- gets a zero-value order rather than a nullable column.
--
-- The reporting consequence is the right one. A redemption appears in the sales
-- report as `code` with nothing in the money columns, because no money arrived
-- at redemption time; the wholesale price is recorded on the batch, where the
-- arrangement actually lives.
ALTER TABLE orders
  DROP CONSTRAINT orders_operation_known;

ALTER TABLE orders
  ADD CONSTRAINT orders_operation_known CHECK (
    operation IN ('purchase', 'upgrade', 'downgrade', 'extension',
                  'topup', 'addon', 'gift', 'goods', 'renewal', 'code')
  );
