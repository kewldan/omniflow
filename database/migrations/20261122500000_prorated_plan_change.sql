-- A plan change that throws the current period away can credit what is left of
-- it, prorated by time at what the customer actually paid (Bedolaga's tariff
-- switch policy). The installation turns it on; off keeps the old rule, where a
-- `replace` upgrade or an `immediate` downgrade simply forfeits the remainder.
ALTER TABLE subscription_policy
  ADD COLUMN prorate_plan_changes boolean NOT NULL DEFAULT false;

-- One credit line per order: the unused value an order hands back to the
-- customer. It is priced when the order is opened and posted to the ledger when
-- the order settles, as a `credit` transaction from platform clearing into the
-- customer's wallet, in the same transaction that spends it. The order keeps
-- its own invariant (wallet + external = subtotal - discount): the credit
-- arrives in the wallet first and the order's wallet part spends it, so every
-- unit of it is a ledger movement rather than a number on the order.
CREATE TABLE order_credits (
  order_id uuid PRIMARY KEY REFERENCES orders (id),
  user_id uuid NOT NULL,
  kind text NOT NULL,
  -- The entitlement whose unused time is credited, and the order that paid
  -- for it.
  source_entitlement_id uuid NOT NULL REFERENCES entitlements (id),
  source_order_id uuid REFERENCES orders (id),
  currency text NOT NULL,
  -- What the customer actually paid for the credited thing, the time it had
  -- left when the credit was priced, and the time that payment bought.
  basis_minor bigint NOT NULL,
  remaining_seconds bigint NOT NULL,
  period_seconds bigint NOT NULL,
  credit_minor bigint NOT NULL,
  ledger_transaction_id uuid UNIQUE REFERENCES ledger_transactions (id),
  credited_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT order_credits_kind_known CHECK (kind IN ('plan_remainder')),
  CONSTRAINT order_credits_currency_shape CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT order_credits_amounts CHECK (
    basis_minor >= 0 AND remaining_seconds >= 0 AND period_seconds > 0
    AND credit_minor >= 0 AND credit_minor <= basis_minor
  ),
  CONSTRAINT order_credits_posted_together CHECK ((ledger_transaction_id IS NULL) = (credited_at IS NULL))
);

-- A period's remainder is handed back once. Two change orders opened side by
-- side against the same entitlement can both be priced, but only one can post.
CREATE UNIQUE INDEX order_credits_remainder_once
  ON order_credits (source_entitlement_id)
  WHERE kind = 'plan_remainder' AND credited_at IS NOT NULL;
CREATE INDEX order_credits_user_idx ON order_credits (user_id, created_at DESC);
