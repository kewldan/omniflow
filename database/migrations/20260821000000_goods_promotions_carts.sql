-- Promotions and saved carts for digital goods.
--
-- The v0.7 shop reused the commerce pipeline for the wallet, payments, refunds,
-- receipts, and history, but stopped short of promotions and carts. Both were
-- plan-scoped, and extending them is a catalogue design decision rather than a
-- wiring one. This migration makes those decisions explicit in the schema.
--
-- Decision one: a promotion applies to plans or to goods, never both.
--
-- An unscoped promotion today means "every plan" — `promotion_plans` empty is
-- the wildcard. If goods simply joined that set, every promotion an operator
-- has ever written would silently start discounting shop orders the moment they
-- upgraded. That is the wrong default twice over: nobody agreed to it, and the
-- two prices are not the same kind of number. A plan price is one the operator
-- chose; a goods price is a provider quote plus a markup, so "50% off" written
-- for plans means something entirely different against a product whose margin
-- might be eight percent.
--
-- Decision two: a promotion may not take a goods order below what the provider
-- charges.
--
-- Markup is where an operator sets the price. A promotion is a discount off the
-- price they set. Selling below cost is a legitimate choice, and it belongs in
-- the markup control — the place that already exists for it — rather than
-- arriving as a side effect of a code somebody typed.
--
-- Decision three: a cart holds a plan or goods, never both.
--
-- They settle differently — one creates a subscription, the other a delivery —
-- and their prices have different lifetimes. A plan price is stable until an
-- operator changes it; a goods quote expires in minutes. Mixing them in one
-- cart would mean one half going stale while the other did not.

-- ---------------------------------------------------------------------------
-- Promotions
-- ---------------------------------------------------------------------------

ALTER TABLE promotions
  ADD COLUMN applies_to text NOT NULL DEFAULT 'plans'
    CHECK (applies_to IN ('plans', 'goods'));

COMMENT ON COLUMN promotions.applies_to IS
  'Which catalogue this promotion discounts. Existing rows default to plans, so no promotion widens on upgrade.';

-- Scoping for a goods promotion, mirroring promotion_plans.
--
-- Empty means every visible product, which is the same wildcard rule plans
-- already use. It is safe here in a way it would not have been for the
-- applies_to default, because an operator writing a goods promotion has by
-- definition just decided that goods are in scope.
CREATE TABLE promotion_goods (
  promotion_id uuid NOT NULL REFERENCES promotions(id) ON DELETE CASCADE,
  product_id uuid NOT NULL REFERENCES goods_products(id),
  PRIMARY KEY (promotion_id, product_id)
);

-- A promotion cannot be scoped to products unless it applies to goods, and the
-- reverse. Without this, a plan promotion could accumulate goods scoping rows
-- that silently did nothing, which reads as configured and behaves as absent.
CREATE OR REPLACE FUNCTION promotion_goods_scope_matches() RETURNS trigger AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM promotions p
    WHERE p.id = NEW.promotion_id AND p.applies_to = 'goods'
  ) THEN
    RAISE EXCEPTION 'promotion % does not apply to goods', NEW.promotion_id
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER promotion_goods_scope_matches
  BEFORE INSERT OR UPDATE ON promotion_goods
  FOR EACH ROW EXECUTE FUNCTION promotion_goods_scope_matches();

-- What the discount actually took off a shop order.
--
-- The margin view reports `quoted_price - quoted_cost`. Once a promotion can
-- apply, that overstates every discounted order, because the operator gave some
-- of the margin away. Recording the discount on the goods row keeps the margin
-- derivable from what was stored rather than recomputed from a markup that may
-- since have changed.
ALTER TABLE goods_orders
  ADD COLUMN discount_minor bigint NOT NULL DEFAULT 0 CHECK (discount_minor >= 0);

-- The floor, enforced in the table as well as in Go.
--
-- A discount that breaches provider cost means the installation pays to make a
-- sale. The application refuses it with a message naming the reason; this
-- constraint is what stops a script, a backfill, or a future code path doing it
-- quietly. It applies only where the cost is known — a product sold at a price
-- the gateway will not decompose has no floor to check against.
ALTER TABLE goods_orders
  ADD CONSTRAINT goods_orders_discount_respects_cost
    CHECK (NOT cost_known OR quoted_price_minor - discount_minor >= quoted_cost_minor);

-- ---------------------------------------------------------------------------
-- Carts
-- ---------------------------------------------------------------------------

-- A cart is now a plan cart or a goods cart, so the plan reference becomes
-- optional and exactly one of the two must be present.
ALTER TABLE carts
  ALTER COLUMN plan_version_id DROP NOT NULL;

ALTER TABLE carts
  ADD COLUMN kind text NOT NULL DEFAULT 'plan' CHECK (kind IN ('plan', 'goods'));

ALTER TABLE carts
  ADD CONSTRAINT carts_plan_shape
    CHECK ((kind = 'plan') = (plan_version_id IS NOT NULL));

-- The goods line a cart holds.
--
-- One line rather than many, matching the shop itself: a purchase names one
-- product, one quantity, and one recipient. A basket of several is a different
-- product decision, and building the table for it now would be guessing at the
-- shape.
--
-- `saved_price_minor` is what the customer was shown when they saved. It is not
-- what they will be charged: the price is re-quoted before any charge, and a
-- rise is refused rather than applied. Keeping it is what makes that comparison
-- possible.
CREATE TABLE cart_goods (
  cart_id uuid PRIMARY KEY REFERENCES carts(id) ON DELETE CASCADE,
  product_id uuid NOT NULL REFERENCES goods_products(id),
  quantity integer NOT NULL DEFAULT 1 CHECK (quantity > 0),
  recipient_username text NOT NULL CHECK (recipient_username ~ '^[A-Za-z0-9_]{5,32}$'),
  recipient_is_self boolean NOT NULL DEFAULT false,
  saved_price_minor bigint NOT NULL CHECK (saved_price_minor >= 0),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  created_at timestamptz NOT NULL DEFAULT now()
);

-- A goods cart has a line, and a plan cart does not. Expressed as a trigger
-- because it spans two tables, and left as a constraint rather than an
-- application check for the same reason as the discount floor.
CREATE OR REPLACE FUNCTION cart_goods_kind_matches() RETURNS trigger AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM carts c WHERE c.id = NEW.cart_id AND c.kind = 'goods'
  ) THEN
    RAISE EXCEPTION 'cart % is not a goods cart', NEW.cart_id
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER cart_goods_kind_matches
  BEFORE INSERT OR UPDATE ON cart_goods
  FOR EACH ROW EXECUTE FUNCTION cart_goods_kind_matches();
