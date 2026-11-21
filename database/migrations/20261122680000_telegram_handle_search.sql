-- Finding a customer by their Telegram @username.
--
-- The bot records the customer's Telegram profile on their identity (first
-- and last name, @username) as they use it. The panel's search and the merge
-- picker look a handle up exactly, case-insensitively; this index makes that
-- one probe instead of a scan of every identity. It is an exact match only:
-- there is still no substring search over customers.
CREATE INDEX identities_telegram_handle_idx
  ON identities (lower(metadata->>'username'))
  WHERE provider = 'telegram' AND status = 'active' AND metadata ? 'username';
