-- A test notification, and the history that was already being recorded.
--
-- `notification_deliveries` has carried the answer to "I never got it" since
-- v0.2: the kind, the class, whether it was sent, when, how many times it
-- failed, and — since the consent work — an error code that says `quiet_hours`,
-- `frequency_cap`, `no_consent`, or `bot_blocked` when the message was never
-- sent on purpose. Nothing read it. No migration is needed to expose that; the
-- surfaces are the whole of it.
--
-- What does need one is the test itself: a delivery an operator triggers to
-- prove the path works for one customer, right now, rather than waiting for
-- something real to happen.

ALTER TABLE notification_deliveries
  DROP CONSTRAINT notification_deliveries_kind_check;

ALTER TABLE notification_deliveries
  ADD CONSTRAINT notification_deliveries_kind_known CHECK (
    kind IN ('expiry', 'traffic', 'renewal', 'grace', 'recovery', 'payment',
             'fulfillment', 'support', 'news', 'announcement', 'incident',
             'maintenance', 'marketing', 'referral', 'trial', 'test')
  );

COMMENT ON COLUMN notification_deliveries.error_code IS
  'Why a message was not sent: a transport failure, or a policy outcome such as quiet_hours, frequency_cap, or no_consent. It is what makes "I never got it" answerable.';
