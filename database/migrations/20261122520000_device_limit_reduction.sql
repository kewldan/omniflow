-- A customer may give back device slots they bought as add-ons part-way
-- through the period and have what is left of them credited. Slots are given
-- back one by one, so a device pack can be released in part: released_slots
-- counts what has gone, and the pack is released outright once all of it has.
ALTER TABLE entitlement_addons
  ADD COLUMN released_slots integer NOT NULL DEFAULT 0,
  ADD CONSTRAINT entitlement_addons_released_slots_range
    CHECK (released_slots >= 0 AND (device_slots IS NULL OR released_slots <= device_slots));

ALTER TABLE orders DROP CONSTRAINT orders_operation_known;
ALTER TABLE orders ADD CONSTRAINT orders_operation_known CHECK (
  operation IN ('purchase', 'upgrade', 'downgrade', 'extension', 'topup', 'addon', 'gift',
                'goods', 'renewal', 'code', 'traffic_reset', 'traffic_change', 'device_reduction')
);

-- A device reduction's credit line says how many slots it gives back, taken
-- from the packs it names in order, newest first.
ALTER TABLE order_credits
  ADD COLUMN released_slots integer NOT NULL DEFAULT 0,
  ADD CONSTRAINT order_credits_released_slots_range CHECK (released_slots >= 0);
