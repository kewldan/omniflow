-- Broadcast templates can carry a picture and inline buttons, as Bedolaga's
-- broadcasts do. The picture is an https URL Telegram fetches; a button opens
-- a URL or a section of the bot. At most six buttons, one per row.
ALTER TABLE message_templates ADD COLUMN photo_url text;
ALTER TABLE message_templates ADD COLUMN buttons jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE message_templates ADD CONSTRAINT message_templates_photo_url_shape CHECK (
  photo_url IS NULL OR (photo_url ~ '^https://' AND char_length(photo_url) <= 500)
);
ALTER TABLE message_templates ADD CONSTRAINT message_templates_buttons_shape CHECK (
  jsonb_typeof(buttons) = 'array' AND jsonb_array_length(buttons) <= 6
);
