-- Selling Telegram Stars the way the Bedolaga fork does: any amount between
-- operator-set bounds, at a fixed price per Star, bought directly on Fragment
-- and paid from the operator's own TON wallet.

-- A Stars product is either a fixed pack (star_quantity) or a custom amount
-- the customer picks between min and max, with optional preset buttons. Never
-- both: a product that is "100 Stars" and also "50 to 10 000" would leave the
-- quantity on an order ambiguous.
ALTER TABLE goods_products
  ADD COLUMN min_star_quantity integer,
  ADD COLUMN max_star_quantity integer,
  ADD COLUMN star_presets integer[] NOT NULL DEFAULT '{}';

ALTER TABLE goods_products DROP CONSTRAINT goods_products_stars_shape;
ALTER TABLE goods_products ADD CONSTRAINT goods_products_stars_amount_shape CHECK (
  (kind = 'telegram_stars') = (star_quantity IS NOT NULL OR min_star_quantity IS NOT NULL)
);
ALTER TABLE goods_products ADD CONSTRAINT goods_products_custom_amount_range CHECK (
  (min_star_quantity IS NULL) = (max_star_quantity IS NULL)
  AND (
    min_star_quantity IS NULL
    OR (star_quantity IS NULL AND min_star_quantity > 0 AND max_star_quantity >= min_star_quantity)
  )
);
ALTER TABLE goods_products ADD CONSTRAINT goods_products_star_presets_bounded CHECK (
  cardinality(star_presets) <= 12
);

-- A fixed price per Star. Like fixed_amount_minor it opts out of deriving the
-- price from the provider's cost, which is what lets a saved purchase of such
-- a product buy itself after a top-up. One operator price at most.
ALTER TABLE goods_pricing ADD COLUMN unit_price_minor bigint;
ALTER TABLE goods_pricing ADD CONSTRAINT goods_pricing_unit_price_positive CHECK (
  unit_price_minor IS NULL OR unit_price_minor > 0
);
ALTER TABLE goods_pricing ADD CONSTRAINT goods_pricing_single_operator_price CHECK (
  fixed_amount_minor IS NULL OR unit_price_minor IS NULL
);

-- The Stars an order delivers, recorded on the order so delivery and the
-- statistics never re-derive it from a product an operator may since have
-- changed, and where the purchase came from.
ALTER TABLE goods_orders
  ADD COLUMN star_quantity integer,
  ADD COLUMN source text NOT NULL DEFAULT 'unknown';
ALTER TABLE goods_orders ADD CONSTRAINT goods_orders_star_quantity_positive CHECK (
  star_quantity IS NULL OR star_quantity > 0
);
ALTER TABLE goods_orders ADD CONSTRAINT goods_orders_source_known CHECK (
  source IN ('bot', 'web', 'auto_cart', 'unknown')
);
CREATE INDEX goods_orders_recipient_search_idx ON goods_orders (lower(recipient_username));

ALTER TABLE cart_goods ADD COLUMN star_quantity integer;
ALTER TABLE cart_goods ADD CONSTRAINT cart_goods_star_quantity_positive CHECK (
  star_quantity IS NULL OR star_quantity > 0
);

-- What the operator actually paid for a delivery, for an adapter that pays
-- the provider itself.
--
-- broadcasting_at is the double-spend guard. It is committed in a transaction
-- of its own immediately before the payment is sent, so a worker that finds
-- it set on a delivery it did not finish knows money may have left and parks
-- the delivery for review instead of paying again.
--
-- settled_cost_minor fixes the cost in the shop currency at the moment of
-- delivery, at that minute's rate, so margin reports do not move with TON.
ALTER TABLE goods_deliveries
  ADD COLUMN broadcasting_at timestamptz,
  ADD COLUMN provider_request_id text,
  ADD COLUMN settlement_asset text,
  ADD COLUMN settlement_cost_nano bigint,
  ADD COLUMN settlement_tx_hash text,
  ADD COLUMN provider_confirmed boolean,
  ADD COLUMN dry_run boolean NOT NULL DEFAULT false,
  ADD COLUMN settled_cost_minor bigint,
  ADD COLUMN settled_rate_minor bigint,
  ADD COLUMN settled_rate_source text;
ALTER TABLE goods_deliveries ADD CONSTRAINT goods_deliveries_settlement_asset_format CHECK (
  settlement_asset IS NULL OR settlement_asset ~ '^[A-Z]{2,10}$'
);
ALTER TABLE goods_deliveries ADD CONSTRAINT goods_deliveries_settlement_cost_nonnegative CHECK (
  settlement_cost_nano IS NULL OR settlement_cost_nano >= 0
);
ALTER TABLE goods_deliveries ADD CONSTRAINT goods_deliveries_settled_cost_nonnegative CHECK (
  settled_cost_minor IS NULL OR settled_cost_minor >= 0
);
ALTER TABLE goods_deliveries ADD CONSTRAINT goods_deliveries_settled_rate_positive CHECK (
  settled_rate_minor IS NULL OR settled_rate_minor > 0
);
ALTER TABLE goods_deliveries ADD CONSTRAINT goods_deliveries_settled_rate_source_known CHECK (
  settled_rate_source IS NULL OR settled_rate_source IN ('manual', 'tonapi', 'coingecko')
);

-- The direct provider is listed, switched off, so an operator can enable it
-- from the panel. It has no stored credential: its secrets are read from the
-- environment only.
INSERT INTO goods_providers (slug, enabled) VALUES ('fragment_direct', false)
ON CONFLICT (slug) DO NOTHING;
