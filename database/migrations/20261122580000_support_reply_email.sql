-- A support reply the bot could not deliver — the customer has no Telegram,
-- blocked the bot, or deleted the account — is mailed to their verified
-- address instead, so a customer who only uses the web cabinet learns there is
-- an answer waiting.
ALTER TABLE email_outbox
  DROP CONSTRAINT email_outbox_kind_known,
  ADD CONSTRAINT email_outbox_kind_known CHECK (kind IN (
    'receipt', 'campaign', 'offer', 'guest_purchase', 'guest_gift', 'withdrawal', 'test', 'maintenance',
    'support_reply'
  ));
