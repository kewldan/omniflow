-- When an order was paid, as a fact that never moves.
--
-- The dashboard reports its fixed thirty-day window against `orders.updated_at`,
-- which is close enough for "roughly how are we doing" and wrong for a report an
-- operator chooses the period of. `updated_at` moves on any write: recording a
-- refund in March silently moves a February sale into March, so the same report
-- run twice over the same closed period returns different numbers.
--
-- A settled sale needs an instant that is set once and never again, which is
-- what this column is. It is nullable because most orders never settle — a
-- draft, a cancelled order, and an expired one have no payment instant, and
-- inventing one would put them in a revenue report.

ALTER TABLE orders ADD COLUMN paid_at timestamptz;

-- Existing settled orders get the best approximation available, which is the
-- last time they changed. That is exactly the figure the dashboard has been
-- using, so nothing gets worse; it stops getting worse from here.
UPDATE orders
SET paid_at = updated_at
WHERE state IN ('paid', 'fulfilled', 'partially_refunded', 'refunded');

-- The reporting queries scan by this column over a bounded window, and the
-- partial index keeps unsettled orders — the majority in a busy installation —
-- out of it entirely.
CREATE INDEX orders_paid_at_idx ON orders (paid_at) WHERE paid_at IS NOT NULL;

COMMENT ON COLUMN orders.paid_at IS
  'Set once, when the order first reaches a settled state. Never updated afterwards, so a later refund cannot move a sale between reporting periods.';
