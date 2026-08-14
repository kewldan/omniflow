-- Pausing a subscription: suspension that preserves the remaining days.
--
-- The lifecycle had cancellation and refund. Both end the entitlement. Neither
-- covers the case people actually ask for — "I am away for six weeks, stop the
-- clock" — and the workaround an operator reaches for otherwise is to disable
-- the Remnawave user, which stops access and does not stop the clock. The
-- customer comes back to a subscription that expired while they could not use
-- it.
--
-- A pause is therefore two facts, not one: the access stops, and the remaining
-- time is preserved. The second is what these columns record.

ALTER TABLE entitlements
  ADD COLUMN paused_at timestamptz,
  -- How long this entitlement has been paused in total, across every pause.
  --
  -- It is not needed to compute the resume — that only needs `paused_at` — and
  -- it is here because it is the only record that `ends_at` was moved for a
  -- reason. Without it, an entitlement whose expiry sits six weeks past what the
  -- order paid for looks like a mistake, and there is nothing to check it
  -- against.
  ADD COLUMN paused_seconds bigint NOT NULL DEFAULT 0 CHECK (paused_seconds >= 0);

ALTER TABLE entitlements
  DROP CONSTRAINT entitlements_status_check;

ALTER TABLE entitlements
  ADD CONSTRAINT entitlements_status_known CHECK (
    status IN ('pending', 'active', 'limited', 'disabled', 'paused',
               'expired', 'superseded', 'failed')
  );

-- `paused` and `paused_at` are one fact recorded twice, so the table refuses to
-- hold them apart.
--
-- This is deliberately strict, and it is aimed at the reconciler: it reads the
-- Remnawave user back and writes the observed status onto the entitlement, and a
-- paused user is a disabled user from Remnawave's side. A reconcile that mapped
-- that back to `disabled` without clearing `paused_at` would silently lose the
-- record that time was owed, and the customer would be short the days. With this
-- constraint that write fails loudly instead.
ALTER TABLE entitlements
  ADD CONSTRAINT entitlements_pause_consistent CHECK (
    (status = 'paused') = (paused_at IS NOT NULL)
  );

-- Two fulfillment operations rather than reusing `disable` and `enable`.
--
-- Resuming is two Remnawave calls that have to happen in that order: push the
-- new expiry, then re-enable. Enqueuing them as two operations would let them
-- run out of order and briefly enable a user whose expiry was still the old,
-- now-past one. One operation that does both keeps the order and stays
-- idempotent, because each underlying call already is.
ALTER TABLE fulfillment_operations
  DROP CONSTRAINT fulfillment_operations_operation_check;

ALTER TABLE fulfillment_operations
  ADD CONSTRAINT fulfillment_operations_operation_known CHECK (
    operation IN ('create', 'extend', 'enable', 'disable', 'reset_traffic',
                  'set_limits', 'set_squads', 'reconcile', 'pause', 'resume')
  );

COMMENT ON COLUMN entitlements.paused_at IS
  'When the current pause began. NULL exactly when the status is not paused; the table enforces the pairing.';
