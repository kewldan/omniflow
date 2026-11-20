-- News categories an operator manages, with a colour, instead of four fixed
-- ones; tags, a cover image, a featured flag, and a view counter on posts —
-- Bedolaga's news editor.
--
-- A category also names the notification kind its posts are announced under
-- (one of the four kinds customers can mute), so news_posts.category keeps
-- holding that kind and every delivery and preference path that reads it is
-- unchanged. The four former categories are seeded with their own kind, and
-- every existing post is placed in the category of the same code.
CREATE TABLE news_categories (
  code text PRIMARY KEY,
  name_ru text NOT NULL,
  name_en text NOT NULL,
  color text NOT NULL,
  kind text NOT NULL,
  sort_order integer NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT news_categories_code_shape CHECK (code ~ '^[a-z][a-z0-9_-]{1,31}$'),
  CONSTRAINT news_categories_names_length CHECK (
    char_length(name_ru) BETWEEN 1 AND 40 AND char_length(name_en) BETWEEN 1 AND 40
  ),
  CONSTRAINT news_categories_color_hex CHECK (color ~ '^#[0-9a-f]{6}$'),
  CONSTRAINT news_categories_kind_known CHECK (kind IN ('news', 'announcement', 'incident', 'maintenance'))
);

INSERT INTO news_categories (code, name_ru, name_en, color, kind, sort_order) VALUES
  ('news', 'Новости', 'News', '#2563eb', 'news', 10),
  ('announcement', 'Объявления', 'Announcements', '#7c3aed', 'announcement', 20),
  ('incident', 'Сбои', 'Incidents', '#dc2626', 'incident', 30),
  ('maintenance', 'Техработы', 'Maintenance', '#d97706', 'maintenance', 40);

ALTER TABLE news_posts ADD COLUMN category_code text REFERENCES news_categories (code);
UPDATE news_posts SET category_code = category;
ALTER TABLE news_posts ALTER COLUMN category_code SET NOT NULL;

-- The notification kind follows the category: a writer names the category and
-- the kind is copied from it, so the two cannot disagree. A writer that still
-- names only the kind lands in the seeded category of the same code.
CREATE FUNCTION news_posts_category_kind() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.category_code IS NULL THEN
    NEW.category_code := NEW.category;
  END IF;
  SELECT kind INTO NEW.category FROM news_categories WHERE code = NEW.category_code;
  RETURN NEW;
END;
$$;

CREATE TRIGGER news_posts_category_kind
  BEFORE INSERT OR UPDATE OF category_code ON news_posts
  FOR EACH ROW EXECUTE FUNCTION news_posts_category_kind();

ALTER TABLE news_posts ADD COLUMN tags text[] NOT NULL DEFAULT '{}';
ALTER TABLE news_posts ADD COLUMN cover_url text;
ALTER TABLE news_posts ADD COLUMN featured boolean NOT NULL DEFAULT false;
-- Opens of the post: every open in the bot and every "mark read" on the web.
-- Distinct readers stay in news_reads.
ALTER TABLE news_posts ADD COLUMN view_count bigint NOT NULL DEFAULT 0;

ALTER TABLE news_posts ADD CONSTRAINT news_posts_tags_bounded CHECK (
  cardinality(tags) <= 10 AND array_to_string(tags, ',') ~ '^([a-zа-яё0-9_]{1,32}(,|$))*$'
);
ALTER TABLE news_posts ADD CONSTRAINT news_posts_cover_https CHECK (
  cover_url IS NULL OR (cover_url ~ '^https://[^\s<>"]+$' AND char_length(cover_url) <= 2000)
);
ALTER TABLE news_posts ADD CONSTRAINT news_posts_view_count_nonnegative CHECK (view_count >= 0);

CREATE INDEX news_posts_category_code ON news_posts (category_code);
CREATE INDEX news_posts_tags ON news_posts USING gin (tags);
