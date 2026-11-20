-- Advertising tags carried through a Telegram /start link (Bedolaga reads
-- UTM and yclid the same way): `utm_<source>__<medium>__<campaign>` and
-- `yclid_<id>` / `gclid_<id>`. A customer keeps the first tags they arrived
-- with, and every order they create afterwards is attributed to them in
-- order_attributions, the table the web checkout writes and the conversion
-- export reads. A web checkout that records its own tags still overrides them.
CREATE TABLE customer_start_attributions (
  user_id uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
  click_id text,
  click_source text,
  utm_source text,
  utm_medium text,
  utm_campaign text,
  recorded_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT customer_start_attributions_click_pair CHECK ((click_id IS NULL) = (click_source IS NULL)),
  CONSTRAINT customer_start_attributions_click_shape CHECK (click_id IS NULL OR click_id ~ '^[A-Za-z0-9_.-]{6,200}$'),
  CONSTRAINT customer_start_attributions_source_known CHECK (click_source IS NULL OR click_source IN ('google', 'yandex')),
  CONSTRAINT customer_start_attributions_lengths CHECK (
    COALESCE(char_length(utm_source), 0) <= 120 AND COALESCE(char_length(utm_medium), 0) <= 120
    AND COALESCE(char_length(utm_campaign), 0) <= 120
  ),
  CONSTRAINT customer_start_attributions_not_empty CHECK (click_id IS NOT NULL OR utm_source IS NOT NULL)
);

CREATE FUNCTION attribute_order_from_start() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  INSERT INTO order_attributions (order_id, click_id, click_source, utm_source, utm_medium, utm_campaign)
  SELECT NEW.id, a.click_id, a.click_source, a.utm_source, a.utm_medium, a.utm_campaign
  FROM customer_start_attributions a WHERE a.user_id = NEW.user_id
  ON CONFLICT (order_id) DO NOTHING;
  RETURN NEW;
END;
$$;
CREATE TRIGGER orders_start_attribution AFTER INSERT ON orders
  FOR EACH ROW EXECUTE FUNCTION attribute_order_from_start();
