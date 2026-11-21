-- Telegram Stars orders a previous bot's shop delivered, carried over as
-- history: who bought stars, how many, for how much, and when. A segment
-- reads it to find a stars buyer without a VPN, and the sales report shows
-- the previous shop's revenue apart from Omniflow's own goods sales. It is
-- history, never a ledger entry or a goods order Omniflow could act on.
CREATE TABLE legacy_goods_orders (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  source text NOT NULL,
  source_id bigint NOT NULL,
  kind text NOT NULL,
  quantity bigint NOT NULL,
  amount_minor bigint NOT NULL,
  currency text NOT NULL,
  delivered_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT legacy_goods_orders_source_known CHECK (source IN ('bedolaga')),
  CONSTRAINT legacy_goods_orders_kind_known CHECK (kind IN ('telegram_stars', 'telegram_premium')),
  CONSTRAINT legacy_goods_orders_quantity_positive CHECK (quantity > 0),
  CONSTRAINT legacy_goods_orders_amount_positive CHECK (amount_minor > 0),
  CONSTRAINT legacy_goods_orders_currency_code CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT legacy_goods_orders_source_row UNIQUE (source, source_id)
);

CREATE INDEX legacy_goods_orders_user ON legacy_goods_orders (user_id, delivered_at);
CREATE INDEX legacy_goods_orders_delivered ON legacy_goods_orders (delivered_at);
