-- Information pages an operator publishes: FAQ, terms, the offer, the privacy
-- policy, and anything else they need at an address of their own.
--
-- Until now the only content surface was news, and terms existed as a
-- `termsUrl` in the branding section pointing somewhere else entirely. That
-- forced an operator to host and translate those documents outside the product,
-- and payment providers and application stores routinely require an offer and a
-- privacy policy at a stable address before they will approve an account.
--
-- These are not news posts, and the difference is not cosmetic. A news post has
-- a publication date, expires, is read once, and counts towards an unread
-- badge. A terms page has none of those: it is a permanent address whose content
-- changes in place, and it has to be readable by somebody who has never signed
-- in — a provider's reviewer, or a customer deciding whether to.

CREATE TABLE info_pages (
  -- The slug is the address. It is the primary key rather than a column beside
  -- a generated identifier because the address is the thing that has to be
  -- stable: a provider approved a URL, and a page that could change its slug
  -- while keeping its identity would break that approval silently.
  slug text PRIMARY KEY CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,48}$'),

  -- What kind of document this is. It exists so the customer panel can link to
  -- "the privacy policy" without an operator having to name the slug the same
  -- way in two places, and so a page a provider requires is findable among the
  -- ones an operator invented.
  kind text NOT NULL CHECK (kind IN ('faq', 'terms', 'offer', 'privacy', 'custom')),

  -- A page with no publication instant is a draft: it answers 404 publicly and
  -- is visible only in the panel. Unpublishing is setting this back to NULL,
  -- which is reversible; deleting the row is not, and takes the address with it.
  published_at timestamptz,

  -- Whether the page appears in the customer panel's own list. A document that
  -- exists to satisfy a provider's review needs a stable address and not
  -- necessarily a menu entry.
  listed boolean NOT NULL DEFAULT true,
  sort_order integer NOT NULL DEFAULT 0,

  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid REFERENCES admin_users(id)
);

CREATE INDEX info_pages_published_idx
  ON info_pages (sort_order, slug) WHERE published_at IS NOT NULL;

CREATE TABLE info_page_localizations (
  page_slug text NOT NULL REFERENCES info_pages(slug) ON DELETE CASCADE,
  locale text NOT NULL CHECK (locale IN ('ru', 'en')),
  title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),

  -- The body is a long document. Forty thousand characters is a terms of
  -- service with room to spare, and a bound rather than unlimited text because
  -- this is served publicly to anybody who asks.
  --
  -- It is stored as the operator's own source text and never as HTML. The API
  -- parses it into a typed block structure and the browser renders text nodes,
  -- so nothing an operator types can become markup on the origin that holds the
  -- session cookie.
  body text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 40000),

  PRIMARY KEY (page_slug, locale)
);

COMMENT ON COLUMN info_page_localizations.body IS
  'Operator source text. Parsed into typed blocks by internal/infopage and never rendered as HTML.';
