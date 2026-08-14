-- The client applications an installation recommends, and the platforms it
-- recommends them for.
--
-- This table was compiled into `internal/commerce`: five platforms and nine
-- entries, shared by the bot and the customer web panel so that a customer
-- reading "install Happ" in a chat and something else in a browser was
-- impossible. The single source is the property worth keeping. What it cost was
-- that adding a client, adding a platform, or writing one platform's
-- instructions needed a release.
--
-- Moving it here keeps the property and drops the cost: one table, read by both
-- surfaces, and the seed below reproduces exactly what was compiled in — so an
-- installation that upgrades and changes nothing recommends what it recommended
-- yesterday.

CREATE TABLE connect_platforms (
  slug text PRIMARY KEY CHECK (slug ~ '^[a-z0-9][a-z0-9_-]{0,30}$'),

  -- The label is stored per locale rather than resolved from a message
  -- catalogue, because an operator who adds a platform has no way to add a
  -- translation key to a compiled catalogue. The two shipped locales are
  -- columns for the same reason the maintenance notice is.
  label_en text NOT NULL CHECK (char_length(label_en) BETWEEN 1 AND 40),
  label_ru text NOT NULL CHECK (char_length(label_ru) BETWEEN 1 AND 40),

  enabled boolean NOT NULL DEFAULT true,
  sort_order integer NOT NULL DEFAULT 0,

  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid REFERENCES admin_users(id)
);

CREATE TABLE connect_clients (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  platform_slug text NOT NULL REFERENCES connect_platforms(slug) ON DELETE CASCADE,

  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60),

  -- The import scheme, concatenated with the subscription link to produce the
  -- deep link a customer presses.
  --
  -- This column is the security question in this table, because its value ends
  -- up in the href of an anchor in the customer web panel. The pattern admits a
  -- scheme followed by `://` and a conservative path alphabet, and the
  -- constraint below names the four schemes that must never appear whatever the
  -- pattern would allow: `javascript:` in an href executes, and `data:`,
  -- `vbscript:`, and `file:` are the neighbours it travels with. Go refuses the
  -- same four, so a script writing directly to this table is refused too.
  scheme text NOT NULL CHECK (scheme ~ '^[a-z][a-z0-9+.-]{1,30}://[A-Za-z0-9/_.~-]{0,60}$'),
  CONSTRAINT connect_clients_scheme_safe CHECK (
    lower(split_part(scheme, ':', 1)) NOT IN ('javascript', 'data', 'vbscript', 'file')
  ),

  -- Where a customer gets the application. HTTPS only: this is rendered as a
  -- link to somebody who is about to install software on their own device.
  --
  -- The whitespace check is separate from the prefix check rather than folded
  -- into one pattern, because a browser strips whitespace before parsing a URL
  -- and a value with a newline in it is not the value an operator reviewed.
  download_url text CHECK (
    download_url IS NULL OR (
      download_url ~ '^https://'
      AND char_length(download_url) BETWEEN 11 AND 300
      AND download_url !~ '[[:space:]]'
    )
  ),

  -- Per-platform, per-locale instructions. Empty means the generic three steps
  -- both surfaces already render, which is what every seeded row uses — so
  -- writing instructions is an improvement an operator can make rather than
  -- work an upgrade forces on them.
  instructions_en text CHECK (instructions_en IS NULL OR char_length(instructions_en) <= 2000),
  instructions_ru text CHECK (instructions_ru IS NULL OR char_length(instructions_ru) <= 2000),

  enabled boolean NOT NULL DEFAULT true,
  sort_order integer NOT NULL DEFAULT 0,

  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid REFERENCES admin_users(id),

  -- One entry per application per platform. Recommending the same client twice
  -- on one screen is a mistake rather than a configuration.
  UNIQUE (platform_slug, name)
);

CREATE INDEX connect_clients_platform_idx
  ON connect_clients (platform_slug, sort_order, name);

-- The seed: exactly what `internal/commerce` had compiled in, in the order it
-- recommended them, with the labels the bot's own message catalogue used —
-- emoji included, because an upgrade that quietly changed the buttons a
-- customer sees would not be the no-op this migration is meant to be.
INSERT INTO connect_platforms (slug, label_en, label_ru, sort_order) VALUES
  ('ios', '🍎 iPhone / iPad', '🍎 iPhone / iPad', 10),
  ('android', '🤖 Android', '🤖 Android', 20),
  ('windows', '🪟 Windows', '🪟 Windows', 30),
  ('macos', '💻 macOS', '💻 macOS', 40),
  ('linux', '🐧 Linux', '🐧 Linux', 50);

INSERT INTO connect_clients (platform_slug, name, scheme, sort_order) VALUES
  ('ios', 'Happ', 'happ://add/', 10),
  ('ios', 'v2RayTun', 'v2raytun://import/', 20),
  ('ios', 'Streisand', 'streisand://import/', 30),
  ('android', 'Happ', 'happ://add/', 10),
  ('android', 'v2RayTun', 'v2raytun://import/', 20),
  ('android', 'Hiddify', 'hiddify://import/', 30),
  ('windows', 'Hiddify', 'hiddify://import/', 10),
  ('windows', 'v2RayTun', 'v2raytun://import/', 20),
  ('macos', 'Happ', 'happ://add/', 10),
  ('macos', 'Streisand', 'streisand://import/', 20),
  ('linux', 'Hiddify', 'hiddify://import/', 10);

COMMENT ON TABLE connect_clients IS
  'Operator-editable connection guidance. Read by the bot and the customer web panel from one place so they cannot recommend different applications.';
