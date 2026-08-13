-- Support attachments say where their bytes are, in a column rather than in a
-- prefix.
--
-- `support_attachments` was designed for Telegram, where the file stays with
-- Telegram and Omniflow stores only the identifier that fetches it. The web
-- panel's upload has no such custodian: the bytes are written to the
-- installation's own attachment directory and reached by a content-addressed
-- key. Until now that key went into `telegram_file_id` behind a `web:` prefix,
-- because the table had nowhere else to put it.
--
-- It was safe — the bot only ever renders an attachment's name and size, and
-- never sends a stored reference back to Telegram — but it made a column named
-- for one system carry values meaningless to it, and it turned "does this
-- installation hold this file" into a string test every reader had to remember
-- to perform. Retention swept on `LIKE 'web:%'`, the download path refused
-- anything without the prefix, and a query that forgot either would have handed
-- a Telegram identifier to a file reader or a local key to Telegram.
--
-- `origin` states which system holds the bytes and `storage_key` carries the
-- local one, so the question is answered by the schema, and the database
-- refuses a row that answers it two ways at once.

-- A local attachment has no Telegram identifier. The original NOT NULL forbade
-- that, which is the constraint the prefix existed to satisfy.
ALTER TABLE support_attachments
  ALTER COLUMN telegram_file_id DROP NOT NULL;

ALTER TABLE support_attachments
  ADD COLUMN origin text NOT NULL DEFAULT 'telegram'
    CONSTRAINT support_attachments_origin_known
    CHECK (origin IN ('telegram', 'local')),
  -- The key is the hex SHA-256 of the content, which is also what names the file
  -- on disk. Constraining its shape here is what keeps a value that could
  -- traverse out of the attachment directory from being stored at all, instead
  -- of relying on every reader to validate it again on the way out.
  ADD COLUMN storage_key text
    CONSTRAINT support_attachments_storage_key_shape
    CHECK (storage_key IS NULL OR storage_key ~ '^[0-9a-f]{64}$');

-- Existing web uploads carry their key behind the prefix; `from 5` drops exactly
-- `web:`. The default above already made every other row explicitly Telegram's.
UPDATE support_attachments
SET origin = 'local',
    storage_key = substring(telegram_file_id from 5),
    telegram_file_id = NULL
WHERE telegram_file_id LIKE 'web:%';

-- Exactly one reference, and it is the one the origin names. Without this the
-- table would accept a local row carrying a Telegram identifier — the state the
-- prefix was preventing by convention and could not prevent by rule.
ALTER TABLE support_attachments
  ADD CONSTRAINT support_attachments_reference_matches_origin CHECK (
    (origin = 'telegram' AND telegram_file_id IS NOT NULL AND storage_key IS NULL)
    OR (origin = 'local' AND storage_key IS NOT NULL AND telegram_file_id IS NULL)
  );

-- `UNIQUE (message_id, telegram_file_id)` still dedupes Telegram attachments and
-- now passes over local ones, because NULLs are distinct. This restores the same
-- guarantee on the other side: one message never carries the same stored file
-- twice.
CREATE UNIQUE INDEX support_attachments_local_file_idx
  ON support_attachments (message_id, storage_key) WHERE storage_key IS NOT NULL;
