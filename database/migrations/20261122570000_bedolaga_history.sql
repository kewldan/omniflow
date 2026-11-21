-- History carried over from the previous bot, so the customers who paid and
-- left before the migration are still findable afterwards.
--
-- Neither table is Omniflow's own record of a sale or a subscription. The
-- money was taken by the previous system and its ledger stays in that
-- system's archived dump; Omniflow keeps only the facts its segments, loyalty
-- tiers, and reports read: who paid, how much, and when; who had a
-- subscription, and when it ended.
--
-- legacy_payments is append-only: the importer inserts a payment once, keyed
-- by its row in the source, and never changes it. The one permitted mutation
-- is moving the rows to another customer when two accounts are merged, which
-- orders get as well.
CREATE TABLE legacy_payments (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  source text NOT NULL,
  source_id bigint NOT NULL,
  method text NOT NULL DEFAULT '',
  amount_minor bigint NOT NULL,
  currency text NOT NULL,
  paid_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT legacy_payments_source_known CHECK (source IN ('bedolaga')),
  CONSTRAINT legacy_payments_amount_positive CHECK (amount_minor > 0),
  CONSTRAINT legacy_payments_currency_code CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT legacy_payments_method_length CHECK (char_length(method) <= 64),
  CONSTRAINT legacy_payments_source_row UNIQUE (source, source_id)
);

CREATE INDEX legacy_payments_user_paid_idx ON legacy_payments (user_id, paid_at);
CREATE INDEX legacy_payments_paid_idx ON legacy_payments (paid_at);

-- Every subscription period the previous bot recorded, ended or not. A
-- subscription that was still running at the migration also became an
-- entitlement; the period is kept beside it so "when did this customer's last
-- subscription end" has one answer for everybody. The importer refreshes a
-- period's dates while the previous bot is still selling, until cutover.
CREATE TABLE legacy_subscription_periods (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  source text NOT NULL,
  source_id bigint NOT NULL,
  is_trial boolean NOT NULL DEFAULT false,
  starts_at timestamptz NOT NULL,
  ends_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT legacy_subscription_periods_source_known CHECK (source IN ('bedolaga')),
  CONSTRAINT legacy_subscription_periods_order CHECK (ends_at >= starts_at),
  CONSTRAINT legacy_subscription_periods_source_row UNIQUE (source, source_id)
);

CREATE INDEX legacy_subscription_periods_user_ends_idx ON legacy_subscription_periods (user_id, ends_at);

-- Where a trial claim came from. A claim made in Omniflow always hangs off the
-- order that claimed it. A claim carried over from the previous bot may have no
-- Omniflow order at all: the trial happened there, or the customer subscribed
-- there and so could never have had one. `bedolaga_history` marks the second
-- kind, which blocks a trial without having been one and so stays out of
-- trial conversion figures.
ALTER TABLE trial_claims
  ADD COLUMN origin text NOT NULL DEFAULT 'omniflow',
  ALTER COLUMN order_id DROP NOT NULL,
  ADD CONSTRAINT trial_claims_origin_known CHECK (origin IN ('omniflow', 'bedolaga_trial', 'bedolaga_history')),
  ADD CONSTRAINT trial_claims_order_required CHECK (order_id IS NOT NULL OR origin <> 'omniflow');
