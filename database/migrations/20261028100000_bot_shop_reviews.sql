-- The digital-goods review screen, held server-side.
--
-- The review screen restates the product and the recipient's username above the
-- button that spends the money, and the buttons used to carry both in their
-- callback data: `shop-buy:<product uuid>:<username>`. Telegram refuses
-- callback data over 64 bytes, and a product identifier leaves room for a
-- username of at most twelve characters, so a longer recipient — most of them —
-- made the whole screen fail to send. The customer was left on the loading
-- state, or, after typing the username, with no answer at all.
--
-- Each rendering of the screen is now one row, and its buttons carry only the
-- row's identifier. A row never changes after it is written, which is what lets
-- an older review message still buy exactly what it showed even after the
-- customer opened another one for a different recipient. Rows expire after a
-- day — the provider quote behind them is shorter-lived than that — and the
-- retention sweep removes them.
CREATE TABLE bot_shop_reviews (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  product_id uuid NOT NULL REFERENCES goods_products(id) ON DELETE CASCADE,
  recipient text NOT NULL CHECK (char_length(recipient) BETWEEN 1 AND 64),
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL DEFAULT now() + interval '1 day'
);

CREATE INDEX bot_shop_reviews_user_idx ON bot_shop_reviews (user_id, expires_at);
CREATE INDEX bot_shop_reviews_expiry_idx ON bot_shop_reviews (expires_at);
