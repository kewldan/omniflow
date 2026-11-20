-- A customer may pay to start their traffic counter afresh before the period
-- ends (Bedolaga's paid traffic reset). The price is a share of the plan
-- version's own price in the order's currency, so it follows every currency
-- the plan is sold in. Null means the version does not sell resets.
ALTER TABLE plan_versions
  ADD COLUMN traffic_reset_price_bps integer,
  ADD CONSTRAINT plan_versions_traffic_reset_price_bps_range
    CHECK (traffic_reset_price_bps IS NULL OR traffic_reset_price_bps BETWEEN 1 AND 100000);

ALTER TABLE orders DROP CONSTRAINT orders_operation_known;
ALTER TABLE orders ADD CONSTRAINT orders_operation_known CHECK (
  operation IN ('purchase', 'upgrade', 'downgrade', 'extension', 'topup', 'addon', 'gift',
                'goods', 'renewal', 'code', 'traffic_reset')
);
