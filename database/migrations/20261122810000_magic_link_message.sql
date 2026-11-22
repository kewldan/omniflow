-- The bot message that delivered a sign-in link, so the link's redemption can
-- delete it from the chat: a spent credential has no reason to stay on the
-- customer's screen, and a deleted message cannot be forwarded later.
ALTER TABLE customer_magic_links
  ADD COLUMN telegram_chat_id bigint,
  ADD COLUMN telegram_message_id integer;
