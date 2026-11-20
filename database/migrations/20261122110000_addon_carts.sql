-- A mid-period add-on (devices, traffic, servers) the wallet cannot cover is
-- saved as the customer's one open cart and bought automatically once a
-- top-up settles, as Bedolaga's add-on cart does. The cart holds its one line
-- in cart_addons, names the subscription it attaches to, and keeps the price
-- the customer was shown: the purchase never charges more than that.
ALTER TABLE carts DROP CONSTRAINT carts_kind_check;
ALTER TABLE carts ADD CONSTRAINT carts_kind_check CHECK (kind IN ('plan', 'goods', 'addon'));

ALTER TABLE carts DROP CONSTRAINT carts_operation_check;
ALTER TABLE carts ADD CONSTRAINT carts_operation_check
  CHECK (operation IN ('purchase', 'upgrade', 'downgrade', 'extension', 'addon'));

ALTER TABLE carts
  ADD COLUMN saved_price_minor bigint,
  ADD CONSTRAINT carts_addon_shape CHECK (
    (kind = 'addon') = (operation = 'addon')
    AND (kind = 'addon') = (saved_price_minor IS NOT NULL)
    AND (kind <> 'addon' OR (subscription_id IS NOT NULL AND saved_price_minor >= 0))
  );
