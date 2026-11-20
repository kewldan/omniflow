-- A customer may switch the subscription's traffic package to a bigger or a
-- smaller one part-way through the period. The packs it replaces are released
-- rather than deleted: the row stays as history, says which order released it,
-- and stops counting towards the allowance, the bought-traffic cap and the
-- lifetime sweep.
ALTER TABLE entitlement_addons
  ADD COLUMN released_at timestamptz,
  ADD COLUMN released_by_order_id uuid REFERENCES orders (id),
  ADD CONSTRAINT entitlement_addons_released_together
    CHECK ((released_at IS NULL) = (released_by_order_id IS NULL));

ALTER TABLE orders DROP CONSTRAINT orders_operation_known;
ALTER TABLE orders ADD CONSTRAINT orders_operation_known CHECK (
  operation IN ('purchase', 'upgrade', 'downgrade', 'extension', 'topup', 'addon', 'gift',
                'goods', 'renewal', 'code', 'traffic_reset', 'traffic_change')
);

-- A capacity change's credit line names the packs it releases, and credits
-- what is left of them at what their orders took. A pack nobody paid for (a
-- gift) is released with a credit of zero, so the line is written either way.
ALTER TABLE order_credits ADD COLUMN released_addon_ids uuid[] NOT NULL DEFAULT '{}';
ALTER TABLE order_credits DROP CONSTRAINT order_credits_kind_known;
ALTER TABLE order_credits ADD CONSTRAINT order_credits_kind_known
  CHECK (kind IN ('plan_remainder', 'addon_remainder'));
