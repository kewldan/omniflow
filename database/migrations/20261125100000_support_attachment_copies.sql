-- A file a customer sends the bot is copied into the installation's attachment
-- store, so the operator desk can show it. The row keeps its Telegram reference
-- and gains the storage key once the copy exists; fetch_attempts bounds the
-- retries for a file Telegram will not hand over.
ALTER TABLE support_attachments
  ADD COLUMN fetch_attempts smallint NOT NULL DEFAULT 0,
  ADD CONSTRAINT support_attachments_fetch_attempts_check CHECK (fetch_attempts >= 0),
  DROP CONSTRAINT support_attachments_reference_matches_origin,
  ADD CONSTRAINT support_attachments_reference_matches_origin CHECK (
    (origin = 'telegram' AND telegram_file_id IS NOT NULL)
    OR (origin = 'local' AND storage_key IS NOT NULL AND telegram_file_id IS NULL)
  );

-- The bot's copy pass: Telegram files still without a copy.
CREATE INDEX support_attachments_pending_copy
  ON support_attachments (created_at)
  WHERE origin = 'telegram' AND storage_key IS NULL;
