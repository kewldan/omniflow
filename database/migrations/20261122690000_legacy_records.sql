-- Everything the previous bot knew that Omniflow has no table of its own for.
--
-- The importer carries customers, balances, subscriptions, payments, pages,
-- tickets, and the rest into their own tables. Every other row of the
-- previous bot's database — wallet transactions, referral earnings,
-- subscription events, promo code uses, notifications, wheel spins, logs —
-- is kept here as it was, so the old database can be retired without losing
-- an answer to "what happened to this customer before".
--
-- Rows are history: nothing in Omniflow reads them to decide anything. The
-- columns that held secrets (passwords, tokens, hashes, VPN credentials,
-- subscription links, IP addresses) are removed before a row is written. A
-- row belongs to a customer when the previous bot said so; anonymizing that
-- customer deletes their rows, and a merge moves them.
CREATE TABLE legacy_records (
  source text NOT NULL,
  kind text NOT NULL,
  source_id text NOT NULL,
  user_id uuid REFERENCES users (id) ON DELETE CASCADE,
  occurred_at timestamptz,
  data jsonb NOT NULL,
  imported_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (source, kind, source_id),
  CONSTRAINT legacy_records_source_known CHECK (source IN ('bedolaga')),
  CONSTRAINT legacy_records_kind_shape CHECK (kind ~ '^[a-z][a-z0-9_]{0,62}$'),
  CONSTRAINT legacy_records_data_object CHECK (jsonb_typeof(data) = 'object')
);

CREATE INDEX legacy_records_user_idx ON legacy_records (user_id, occurred_at DESC NULLS LAST)
  WHERE user_id IS NOT NULL;
