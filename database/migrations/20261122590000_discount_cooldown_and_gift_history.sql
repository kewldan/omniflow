-- When a customer last had a discount in the previous bot: a one-off discount
-- promo code or a claimed discount offer. A promotion whose eligibility sets
-- discountCooldownDays reads it beside Omniflow's own redemptions, so the
-- "a discount at most once in N days" rule holds across the migration.
--
-- One row per customer, the latest moment. The importer only ever moves it
-- later; a merge keeps the later of the two customers' dates.
CREATE TABLE legacy_customer_discounts (
  user_id uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
  source text NOT NULL,
  last_discount_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT legacy_customer_discounts_source_known CHECK (source IN ('bedolaga'))
);

-- A gift a customer bought in the previous bot's web cabinet was paid straight
-- through a provider, never through the balance, so it is a payment of its
-- own. Its source row is in another table, hence a source of its own.
ALTER TABLE legacy_payments
  DROP CONSTRAINT legacy_payments_source_known,
  ADD CONSTRAINT legacy_payments_source_known CHECK (source IN ('bedolaga', 'bedolaga_gift'));
