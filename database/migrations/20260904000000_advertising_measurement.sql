-- Advertising measurement: the operator's own, never the project's.
--
-- Two things are being added, and they are separate on purpose.
--
-- The settings section holds counter identifiers and webmaster verification
-- tags. It is off by default and stores nothing until an operator turns it on.
--
-- `order_attributions` is the part nothing could do before. Payment here happens
-- on the backend, often a day after the click and sometimes through a transfer
-- an operator confirms by hand, so the browser session that carried the
-- advertising platform's click identifier is long gone by the time there is any
-- money to attribute. A counter script sees the visit and never sees the sale.
-- Carrying the identifier onto the order is what makes an offline conversion
-- upload possible at all.

ALTER TABLE installation_settings
  DROP CONSTRAINT installation_settings_section_known;

ALTER TABLE installation_settings
  ADD CONSTRAINT installation_settings_section_known CHECK (
    section IN ('branding', 'remnawave', 'telegram', 'operator_group',
                'required_channels', 'maintenance', 'notifications', 'telemetry',
                'backup', 'security', 'ai', 'mcp', 'customer_auth', 'theme',
                'analytics')
  );

-- The row exists from the start with an empty document, like every other
-- section. A settings screen that 404s until somebody saves once is a screen
-- nobody can open to make the first save.
INSERT INTO installation_settings (section) VALUES ('analytics');

-- One row per order, not per customer.
--
-- An order is a conversion: it has an amount and a settlement date, which is
-- exactly what an advertising platform's offline upload takes. A customer is a
-- person, and a click identifier held against a person for the life of their
-- account is a profile — a different thing, with different obligations, that
-- none of this needs.
--
-- It cascades with the order. An order deleted for any reason takes its
-- advertising origin with it, and a customer erasure that removes orders
-- removes these without anybody having to remember a second table.
CREATE TABLE order_attributions (
  order_id     uuid PRIMARY KEY REFERENCES orders(id) ON DELETE CASCADE,
  click_id     text,
  click_source text,
  utm_source   text,
  utm_medium   text,
  utm_campaign text,
  utm_content  text,
  utm_term     text,
  recorded_at  timestamptz NOT NULL DEFAULT now(),

  -- A row with nothing in it is a record that somebody visited, which is not
  -- what this table is for.
  CONSTRAINT order_attributions_not_empty CHECK (
    click_id IS NOT NULL OR utm_source IS NOT NULL OR utm_medium IS NOT NULL
    OR utm_campaign IS NOT NULL OR utm_content IS NOT NULL OR utm_term IS NOT NULL
  ),
  -- The identifier is opaque and bounded. The application validates its shape;
  -- this is the floor under that, so a direct write cannot put a newline into a
  -- value that later becomes a cell in a CSV somebody uploads.
  CONSTRAINT order_attributions_click_shape CHECK (
    click_id IS NULL OR click_id ~ '^[A-Za-z0-9_.-]{6,200}$'
  ),
  CONSTRAINT order_attributions_source_known CHECK (
    click_source IS NULL
    OR click_source IN ('google', 'yandex', 'meta', 'microsoft', 'tiktok', 'x')
  ),
  -- A click identifier without the platform that issued it cannot be uploaded
  -- anywhere, so the pair travels together or not at all.
  CONSTRAINT order_attributions_click_pair CHECK (
    (click_id IS NULL) = (click_source IS NULL)
  ),
  CONSTRAINT order_attributions_campaign_length CHECK (
    coalesce(char_length(utm_source), 0) <= 120
    AND coalesce(char_length(utm_medium), 0) <= 120
    AND coalesce(char_length(utm_campaign), 0) <= 120
    AND coalesce(char_length(utm_content), 0) <= 120
    AND coalesce(char_length(utm_term), 0) <= 120
  )
);

-- The export walks settled orders in a period and joins this, so the useful
-- index is the one that finds attributed orders by platform.
CREATE INDEX order_attributions_source_idx ON order_attributions (click_source)
  WHERE click_id IS NOT NULL;

COMMENT ON TABLE order_attributions IS
  'Where one order came from, for the operator''s own advertising measurement. Never sent anywhere by this software; an operator exports a file and uploads it themselves.';
