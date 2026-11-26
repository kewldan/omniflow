-- A customer can send the bot a voice message or an audio file with a support
-- request. They are kept like photos and documents: a Telegram reference that
-- the bot copies into the attachment store, so the desk can play it.
ALTER TABLE support_attachments
  DROP CONSTRAINT support_attachments_kind_check,
  ADD CONSTRAINT support_attachments_kind_check
    CHECK (kind = ANY (ARRAY['photo'::text, 'document'::text, 'voice'::text, 'audio'::text]));
