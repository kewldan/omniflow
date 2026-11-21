-- A payment the previous bot opened that settled after the cutover.
--
-- The provider's webhook now reaches Omniflow, which never opened that
-- payment and used to refuse it: the customer's money was taken and nothing
-- arrived. Omniflow now fetches such a payment through the provider's
-- authenticated API, and when its metadata names a customer the import carried
-- over, credits the amount to that customer's wallet — the previous bot would
-- have topped up the balance with it. One it cannot attribute lands here for
-- an operator instead of being dropped.
--
-- One row per provider payment. `credited` rows are the record of what was
-- paid out; `needs_review` rows are the operator's queue until an operator
-- marks them `resolved` with a note.
CREATE TABLE bedolaga_payment_claims (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  provider text NOT NULL,
  provider_reference text NOT NULL,
  bedolaga_user_id bigint,
  user_id uuid REFERENCES users (id) ON DELETE SET NULL,
  amount_minor bigint NOT NULL,
  currency text NOT NULL,
  payment_type text NOT NULL DEFAULT '',
  status text NOT NULL,
  reason text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz,
  resolved_by uuid REFERENCES admin_users (id),
  resolution_note text,
  CONSTRAINT bedolaga_payment_claims_provider_payment UNIQUE (provider, provider_reference),
  CONSTRAINT bedolaga_payment_claims_status_known CHECK (status IN ('credited', 'needs_review', 'resolved')),
  CONSTRAINT bedolaga_payment_claims_amount_nonnegative CHECK (amount_minor >= 0),
  CONSTRAINT bedolaga_payment_claims_currency_code CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT bedolaga_payment_claims_credited_customer CHECK (status <> 'credited' OR user_id IS NOT NULL),
  CONSTRAINT bedolaga_payment_claims_resolution CHECK (
    (status = 'resolved') = (resolved_at IS NOT NULL)
    AND (resolution_note IS NULL OR char_length(resolution_note) <= 500)
  )
);

CREATE INDEX bedolaga_payment_claims_review_idx ON bedolaga_payment_claims (created_at)
  WHERE status = 'needs_review';
