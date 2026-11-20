-- A customer may remove a paid squad (country) add-on part-way through the
-- period and have what is left of it credited. The pack is released like a
-- replaced traffic package, and the squads it alone granted leave the
-- entitlement through the fulfillment pipeline.
ALTER TABLE orders DROP CONSTRAINT orders_operation_known;
ALTER TABLE orders ADD CONSTRAINT orders_operation_known CHECK (
  operation IN ('purchase', 'upgrade', 'downgrade', 'extension', 'topup', 'addon', 'gift',
                'goods', 'renewal', 'code', 'traffic_reset', 'traffic_change', 'device_reduction',
                'squad_removal')
);
