-- Indexes for the customer panel's live stream.
--
-- An open customer panel asks every few seconds when the customer's own
-- orders, subscriptions, and entitlements last changed. Each lookup is by
-- customer and newest change, which these indexes answer without reading
-- anybody else's rows.
CREATE INDEX orders_customer_updated_idx ON orders (user_id, updated_at DESC);
CREATE INDEX subscriptions_customer_updated_idx ON subscriptions (user_id, updated_at DESC);
CREATE INDEX entitlements_customer_updated_idx ON entitlements (user_id, updated_at DESC);
