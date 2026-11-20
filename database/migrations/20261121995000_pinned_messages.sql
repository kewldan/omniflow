-- Bedolaga's pinned message: one message an operator sends to every
-- customer, optionally pinned in the chat and shown on /start to whoever has
-- not received it, and unpinned for everybody when it is retired.
CREATE TABLE pinned_messages (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  text_ru text NOT NULL,
  text_en text NOT NULL,
  photo_url text,
  pin boolean NOT NULL DEFAULT true,
  show_on_start boolean NOT NULL DEFAULT true,
  status text NOT NULL DEFAULT 'active',
  created_by uuid REFERENCES admin_users (id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  retired_at timestamptz,
  CONSTRAINT pinned_messages_status_known CHECK (status IN ('active', 'retired')),
  CONSTRAINT pinned_messages_retired_consistent CHECK ((status = 'retired') = (retired_at IS NOT NULL)),
  CONSTRAINT pinned_messages_text_length CHECK (
    char_length(text_ru) BETWEEN 1 AND 3500 AND char_length(text_en) BETWEEN 1 AND 3500
  ),
  CONSTRAINT pinned_messages_photo_shape CHECK (photo_url IS NULL OR (photo_url ~ '^https://' AND char_length(photo_url) <= 500))
);
CREATE UNIQUE INDEX pinned_messages_one_active ON pinned_messages ((true)) WHERE status = 'active';

CREATE TABLE pinned_message_deliveries (
  pinned_id uuid NOT NULL REFERENCES pinned_messages (id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  chat_id bigint NOT NULL,
  telegram_message_id bigint,
  status text NOT NULL,
  pinned boolean NOT NULL DEFAULT false,
  unpinned_at timestamptz,
  error_code text,
  delivered_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (pinned_id, user_id),
  CONSTRAINT pinned_message_deliveries_status_known CHECK (status IN ('sent', 'failed'))
);
CREATE INDEX pinned_message_deliveries_to_unpin ON pinned_message_deliveries (pinned_id) WHERE pinned AND unpinned_at IS NULL;
