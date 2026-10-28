-- Telegram delivery health learns a state for a chat that was never opened.
--
-- Telegram answers 403 "bot can't initiate conversation with a user" to a
-- message for somebody who has never pressed Start — a customer imported with
-- a Telegram identity, or one who signed in on the web with the Telegram
-- widget. That was recorded as `blocked`, which was untrue and, worse, final:
-- blocked customers are left out of every notification pass, so nothing would
-- ever be sent to find out otherwise.
--
-- `not_started` is kept apart from `blocked` because they are different facts
-- with the same remedy: both end when the customer writes to the bot, and the
-- bot now clears either the moment any update from that account arrives.
ALTER TABLE bot_delivery_state
  DROP CONSTRAINT bot_delivery_state_status_check;

ALTER TABLE bot_delivery_state
  ADD CONSTRAINT bot_delivery_state_status_known CHECK (
    status IN ('active', 'failing', 'blocked', 'deactivated', 'not_started')
  );
