-- Steam top-ups sold through the digital-goods shop.
--
-- A Steam top-up is a third product kind beside Telegram Premium and Stars. It
-- is always a custom amount: the customer picks how many whole units of the
-- provider's currency to add, inside the product's bounds, so it reuses the
-- columns a custom-amount Stars product uses rather than adding a parallel set
-- every order, cart, and delivery path would have to learn.
ALTER TABLE goods_products DROP CONSTRAINT goods_products_kind_check;
ALTER TABLE goods_products ADD CONSTRAINT goods_products_kind_known
  CHECK (kind IN ('telegram_premium', 'telegram_stars', 'steam_topup'));

ALTER TABLE goods_products DROP CONSTRAINT goods_products_stars_amount_shape;
ALTER TABLE goods_products ADD CONSTRAINT goods_products_amount_shape
  CHECK ((kind IN ('telegram_stars', 'steam_topup')) = (star_quantity IS NOT NULL OR min_star_quantity IS NOT NULL));

ALTER TABLE goods_products ADD CONSTRAINT goods_products_steam_custom_amount
  CHECK (kind <> 'steam_topup' OR (min_star_quantity IS NOT NULL AND star_quantity IS NULL));

COMMENT ON COLUMN goods_products.star_quantity IS
  'Stars in a fixed pack. Never set for a Steam top-up, which is always a custom amount.';
COMMENT ON COLUMN goods_products.min_star_quantity IS
  'Smallest custom amount: Stars, or whole units of the provider''s currency for a Steam top-up.';
COMMENT ON COLUMN goods_products.max_star_quantity IS
  'Largest custom amount: Stars, or whole units of the provider''s currency for a Steam top-up.';

-- The recipient of a Steam top-up is a Steam login: three to sixty-four
-- letters, digits, and underscores. Telegram's stricter rule for usernames
-- stays in the application, which knows which kind of product it is checking.
ALTER TABLE goods_orders DROP CONSTRAINT goods_orders_recipient_username_check;
ALTER TABLE goods_orders ADD CONSTRAINT goods_orders_recipient_format
  CHECK (recipient_username ~ '^[A-Za-z0-9_]{3,64}$');
COMMENT ON COLUMN goods_orders.recipient_username IS
  'Telegram username the goods go to, or the Steam login a Steam top-up credits.';
COMMENT ON COLUMN goods_orders.star_quantity IS
  'Stars the order delivers, or whole units of the provider''s currency a Steam top-up adds.';

ALTER TABLE cart_goods DROP CONSTRAINT cart_goods_recipient_username_check;
ALTER TABLE cart_goods ADD CONSTRAINT cart_goods_recipient_format
  CHECK (recipient_username ~ '^[A-Za-z0-9_]{3,64}$');
