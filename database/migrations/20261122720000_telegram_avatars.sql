-- Customers' Telegram profile photos, kept so the panels serve them from
-- here: Telegram's own picture hosts are not reachable from every network,
-- and a stored copy still answers when the Bot API does not. A row with no
-- photo records that the bot could not see one, so it is not asked again on
-- every view. Erased with the rest of a customer's data on anonymisation.
CREATE TABLE telegram_avatars (
  telegram_id bigint PRIMARY KEY,
  photo bytea,
  fetched_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT telegram_avatars_photo_size CHECK (photo IS NULL OR octet_length(photo) <= 524288)
);
