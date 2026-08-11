-- Recurring renewal: what the charge worker needs that v0.7's first migration
-- did not yet carry.
--
-- Two facts are added, both because the alternative was to re-derive a decision
-- in SQL that the domain package already makes.

-- Whether this failure earns a message.
--
-- `internal/recurring` decides it: the first decline is usually resolved by the
-- retry, and a notification per attempt teaches a customer to ignore them, so
-- only the second failure and the final abandonment are worth sending. Storing
-- the decision rather than recomputing it in the notifier's query keeps that
-- policy in one place, and keeps the query a plain filter that cannot drift
-- away from the Go rule it mirrors.
ALTER TABLE dunning_attempts
  ADD COLUMN notify_required boolean NOT NULL DEFAULT false;

-- The notifier reads exactly one shape: failures that want a message and have
-- not had one. A partial index keeps that read cheap on an installation where
-- the vast majority of attempts succeeded and were never notified about.
CREATE INDEX dunning_attempts_notify_idx
  ON dunning_attempts (occurred_at)
  WHERE notify_required AND notified_at IS NULL;

-- Renewal is now an order operation in its own right.
--
-- v0.5 folded renewals into `extension`, which was accurate — a renewal does
-- extend an entitlement — but it made an automatic charge indistinguishable
-- from one a customer made by hand. Support answers "why was I charged?" from
-- this column, and the honest answer differs between the two, so they stop
-- sharing a value.
ALTER TABLE orders
  DROP CONSTRAINT orders_operation_known;

ALTER TABLE orders
  ADD CONSTRAINT orders_operation_known CHECK (
    operation IN ('purchase', 'upgrade', 'downgrade', 'extension',
                  'topup', 'addon', 'gift', 'goods', 'renewal')
  );
