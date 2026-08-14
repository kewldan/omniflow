-- White-label theming: a palette an operator owns, and the images that carry
-- their mark.
--
-- Until now `branding` held a service name, a support contact, three URLs, a
-- locale, and a timezone. An operator installing Omniflow to sell under their
-- own brand shipped somebody else's colour and type, and there was no
-- configuration that changed it.
--
-- Two storage decisions, and they differ because the material differs.
--
-- The palette is an installation setting like every other section: a document,
-- a version guard, an audit event. It gets its own section rather than more
-- keys on `branding` because it is a different screen with a different
-- question — "how do we look" against "what are we called" — and because a
-- section is what one screen saves atomically.
--
-- The images are bytes, which no settings document should ever carry. A logo
-- inlined as base64 into a JSONB document would be read back on every settings
-- request, would appear in a diagnostics bundle, and would make an audit
-- event's before/after metadata megabytes wide.

ALTER TABLE installation_settings
  DROP CONSTRAINT installation_settings_section_known;

ALTER TABLE installation_settings
  ADD CONSTRAINT installation_settings_section_known CHECK (section IN (
    'branding', 'remnawave', 'telegram', 'operator_group', 'required_channels',
    'maintenance', 'notifications', 'telemetry', 'backup', 'security', 'ai', 'mcp',
    'customer_auth', 'theme'
  ));

INSERT INTO installation_settings (section) VALUES ('theme');

-- The images an installation renders as its own.
--
-- One row per slot rather than a general asset library: there are exactly three
-- places an image appears, each has a fixed meaning, and a library would need a
-- name, a lifecycle, and a way to be referenced from a document — which is a
-- content management system, not a brand.
CREATE TABLE branding_assets (
  kind text PRIMARY KEY CHECK (kind IN ('logo_light', 'logo_dark', 'favicon')),

  -- The allowed types are the ones a browser renders inline without a plugin
  -- and without script. SVG is deliberately absent: an SVG document can carry
  -- <script> and external references, and this file is served from the same
  -- origin as both panels, so accepting one would be a stored cross-site
  -- scripting surface handed to whoever can reach the settings screen.
  content_type text NOT NULL CHECK (content_type IN ('image/png', 'image/jpeg', 'image/webp')),

  bytes bytea NOT NULL CHECK (octet_length(bytes) BETWEEN 1 AND 262144),

  -- The SHA-256 of the bytes, hex encoded. It is the cache validator: the
  -- public asset route serves it as a strong ETag, so a browser that already
  -- holds the logo revalidates in one conditional request rather than
  -- re-downloading it on every page.
  checksum text NOT NULL CHECK (checksum ~ '^[0-9a-f]{64}$'),

  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid REFERENCES admin_users(id)
);

COMMENT ON TABLE branding_assets IS
  'Operator-supplied brand images, served publicly by checksum. Never customer data.';
