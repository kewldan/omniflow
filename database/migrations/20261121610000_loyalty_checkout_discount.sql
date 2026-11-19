-- The loyalty tier's discount is part of the price, not only a figure on the
-- loyalty screen (Bedolaga's promo-group discount). An order records how much
-- of its discount the tier gave and which tier it was, so support can explain
-- the price after the customer's tier has moved on.
ALTER TABLE orders ADD COLUMN loyalty_discount_minor bigint NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN loyalty_tier_code text;
ALTER TABLE orders ADD CONSTRAINT orders_loyalty_discount_bounded CHECK (
  loyalty_discount_minor >= 0 AND loyalty_discount_minor <= discount_minor
);
ALTER TABLE orders ADD CONSTRAINT orders_loyalty_tier_present CHECK (
  (loyalty_discount_minor = 0) OR (loyalty_tier_code IS NOT NULL)
);
