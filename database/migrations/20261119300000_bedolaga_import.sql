-- Bookkeeping for the one-way migration from a Bedolaga bot database
-- (cmd/migrate-bedolaga).
--
-- The importer writes customers, wallets, subscriptions and referrals through
-- the ordinary tables. These two tables are only its memory: which Bedolaga
-- record became which Omniflow row, and what each apply run found. That memory
-- is what makes a second run — after a crash, or the final delta in the
-- cutover window — converge instead of creating a second customer.

CREATE TABLE bedolaga_import_runs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  actor text NOT NULL,
  status text NOT NULL DEFAULT 'running',
  report jsonb NOT NULL DEFAULT '{}'::jsonb,
  started_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz,
  CONSTRAINT bedolaga_import_runs_actor_length CHECK (char_length(actor) BETWEEN 1 AND 120),
  CONSTRAINT bedolaga_import_runs_status_known CHECK (status IN ('running', 'completed', 'failed')),
  CONSTRAINT bedolaga_import_runs_finish_consistent CHECK ((status = 'running') = (finished_at IS NULL))
);

-- One row per Bedolaga record the importer has decided about. The primary key
-- is the Bedolaga identifier, so the mapping survives any number of runs.
--
-- A conflict is not terminal: the next run re-evaluates it, which is how an
-- operator fixes the source data and tries again. Only `applied` is final, and
-- the queries that write this table never move a row out of it.
CREATE TABLE bedolaga_import_items (
  kind text NOT NULL,
  source_id bigint NOT NULL,
  status text NOT NULL,
  codes text[] NOT NULL DEFAULT '{}'::text[],
  user_id uuid REFERENCES users(id),
  subscription_id uuid REFERENCES subscriptions(id),
  entitlement_id uuid REFERENCES entitlements(id),
  fingerprint bytea NOT NULL,
  last_run_id uuid NOT NULL REFERENCES bedolaga_import_runs(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  applied_at timestamptz,
  PRIMARY KEY (kind, source_id),
  CONSTRAINT bedolaga_import_items_kind_known CHECK (kind IN ('user', 'subscription', 'referral')),
  CONSTRAINT bedolaga_import_items_status_known CHECK (status IN ('applied', 'conflict', 'skipped')),
  CONSTRAINT bedolaga_import_items_applied_complete CHECK (
    (status = 'applied') = (applied_at IS NOT NULL)
    AND (status <> 'applied' OR user_id IS NOT NULL)
  ),
  CONSTRAINT bedolaga_import_items_fingerprint_length CHECK (octet_length(fingerprint) = 32)
);

CREATE INDEX bedolaga_import_items_user_idx ON bedolaga_import_items (user_id) WHERE user_id IS NOT NULL;
CREATE INDEX bedolaga_import_items_status_idx ON bedolaga_import_items (kind, status);
