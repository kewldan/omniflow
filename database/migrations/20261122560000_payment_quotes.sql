-- A payment collected in a currency other than the order's: Overpay's
-- international terminal takes euros for a ruble order. The quote is what the
-- provider was asked to collect against what the order costs, fixed when the
-- checkout is opened, so a rate changed in between moves nothing. Settlement
-- maps the money received back onto the order's currency through it: the exact
-- quoted amount settles the order exactly, any other amount is converted at the
-- quote's own ratio and classified like any short or long payment.
--
-- Append-only. One quote per intent; the intent never asks for a second.
CREATE TABLE payment_quotes (
  payment_intent_id uuid PRIMARY KEY REFERENCES payment_intents (id),
  amount_minor bigint NOT NULL,
  currency text NOT NULL,
  charged_minor bigint NOT NULL,
  charged_currency text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT payment_quotes_amount_positive CHECK (amount_minor > 0 AND charged_minor > 0),
  CONSTRAINT payment_quotes_currencies CHECK (
    currency ~ '^[A-Z]{3}$' AND charged_currency ~ '^[A-Z]{3}$' AND currency <> charged_currency
  )
);
