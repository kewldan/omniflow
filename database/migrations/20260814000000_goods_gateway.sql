-- Omniflow v0.7 digital-goods gateway corrections.
--
-- The v0.7 delivery model assumed a provider that honours an idempotency key
-- and reports success only when it has actually delivered. The gateway that
-- fronts Fragment does neither, and both gaps need a state the schema can hold
-- rather than a convention the worker has to remember.
--
-- **No provider-side idempotency.** Submitting a purchase creates and processes
-- it immediately; there is no key to replay against. A request that times out
-- or dies mid-flight is therefore genuinely ambiguous — the goods may or may
-- not have been sent — and retrying it could deliver twice. Such a delivery is
-- neither retried nor refunded automatically: it stops and waits for a person.
--
-- **Success is not proof of delivery.** The gateway answers 200 for a purchase
-- its own processor abandoned, because that processor reports insufficient
-- funds, an expired Fragment session, and a rejected purchase to an operator
-- chat and then returns normally. Until the gateway distinguishes those, an
-- accepted submission means "the gateway took it", not "the recipient has it".

-- ---------------------------------------------------------------------------
-- Ambiguous delivery outcomes
-- ---------------------------------------------------------------------------

-- `ambiguous` is deliberately outside both the retry set and the refund set.
-- Every other class answers "try again" or "give the money back"; this one
-- answers "nobody may decide that automatically", which is the only safe answer
-- when the request may already have spent the operator's funds.
ALTER TABLE goods_deliveries
  DROP CONSTRAINT goods_deliveries_failure_class_check;

ALTER TABLE goods_deliveries
  ADD CONSTRAINT goods_deliveries_failure_class_known CHECK (
    failure_class IS NULL
    OR failure_class IN ('retryable', 'permanent', 'recipient_invalid',
                         'provider_balance', 'provider_unavailable', 'ambiguous')
  );

-- `needs_review` parks a delivery outside the due queue. The partial index that
-- feeds the worker selects only 'pending' and 'submitted', so a parked row is
-- never picked up again by anything except an operator.
ALTER TABLE goods_deliveries
  DROP CONSTRAINT goods_deliveries_status_check;

ALTER TABLE goods_deliveries
  ADD CONSTRAINT goods_deliveries_status_known CHECK (
    status IN ('pending', 'submitted', 'delivered', 'failed', 'cancelled', 'needs_review')
  );

ALTER TABLE goods_orders
  DROP CONSTRAINT goods_orders_status_check;

ALTER TABLE goods_orders
  ADD CONSTRAINT goods_orders_status_known CHECK (
    status IN ('quoted', 'paid', 'delivering', 'delivered', 'failed', 'refunded', 'needs_review')
  );

CREATE INDEX goods_deliveries_review_idx
  ON goods_deliveries (updated_at DESC) WHERE status = 'needs_review';

-- ---------------------------------------------------------------------------
-- Unpriced products
-- ---------------------------------------------------------------------------

-- Telegram Premium has no cost endpoint on the gateway: its prices live in the
-- gateway's own table and are not published. An operator therefore configures
-- the sale price in the panel, and Omniflow has no cost to compare it against.
--
-- Recording that explicitly matters because `quoted_cost_minor` is zero in that
-- case, and a zero cost would otherwise read as a hundred-per-cent margin. The
-- flag lets the finance view say "unknown" instead of reporting a number that
-- is not true.
ALTER TABLE goods_orders
  ADD COLUMN cost_known boolean NOT NULL DEFAULT true;

COMMENT ON COLUMN goods_orders.cost_known IS
  'False when the provider publishes no cost for this product and the operator set the price directly. Margin is unknown, not zero.';
