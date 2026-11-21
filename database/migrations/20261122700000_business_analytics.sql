-- Business analytics: the history the dashboard cannot reconstruct by itself.
--
-- The dashboard reads the present. Monthly recurring revenue, churn, and the
-- shape of the base over time need yesterday's state too, and an entitlement
-- whose end moved — a refund, a pause — no longer says what was active on a
-- given day. One row per day, plan, billing period, kind, and currency keeps
-- that answer as it was when the day closed.
--
-- `kind` separates what was bought from what was given: `paid` carries a
-- non-zero price, `imported` came over from the previous bot (paid there, with
-- no amount here, so its recurring revenue is an estimate from the list
-- price), `trial` is a claimed trial, and `free` is any other zero-value
-- grant. Trials and free grants carry no currency and no revenue.
CREATE TABLE subscription_daily_snapshots (
  day date NOT NULL,
  plan_code text NOT NULL,
  billing_period text NOT NULL,
  kind text NOT NULL,
  currency text NOT NULL DEFAULT '',
  active integer NOT NULL,
  mrr_minor bigint NOT NULL DEFAULT 0,
  taken_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT subscription_daily_snapshots_pkey PRIMARY KEY (day, plan_code, billing_period, kind, currency),
  CONSTRAINT subscription_daily_snapshots_kind_known CHECK (kind IN ('paid', 'imported', 'trial', 'free')),
  CONSTRAINT subscription_daily_snapshots_active_check CHECK (active >= 0),
  CONSTRAINT subscription_daily_snapshots_mrr_check CHECK (mrr_minor >= 0),
  CONSTRAINT subscription_daily_snapshots_currency_shape CHECK (currency = '' OR currency ~ '^[A-Z]{3}$')
);

-- The days a snapshot was taken for, empty or not. A day on which nothing was
-- active has no rows above, and without this a backfill would recompute it on
-- every pass.
CREATE TABLE subscription_snapshot_days (
  day date PRIMARY KEY,
  taken_at timestamptz NOT NULL DEFAULT now()
);

-- Remnawave keeps a node's traffic counter and its online count for now only.
-- An hourly sample is what turns "which node is filling up" into a trend.
-- The counter is cumulative and resets with the node's cycle; readers take the
-- positive differences between samples.
CREATE TABLE node_traffic_samples (
  node_uuid text NOT NULL,
  sampled_at timestamptz NOT NULL,
  node_name text NOT NULL,
  country_code text NOT NULL DEFAULT '',
  connected boolean NOT NULL,
  traffic_used_bytes bigint NOT NULL,
  traffic_limit_bytes bigint NOT NULL DEFAULT 0,
  users_online integer,
  CONSTRAINT node_traffic_samples_pkey PRIMARY KEY (node_uuid, sampled_at),
  CONSTRAINT node_traffic_samples_used_check CHECK (traffic_used_bytes >= 0),
  CONSTRAINT node_traffic_samples_limit_check CHECK (traffic_limit_bytes >= 0),
  CONSTRAINT node_traffic_samples_online_check CHECK (users_online IS NULL OR users_online >= 0)
);

CREATE INDEX node_traffic_samples_time_idx ON node_traffic_samples (sampled_at);

-- What an operator paid for advertising. Omniflow sees the revenue an
-- advertisement brought and never the invoice for it, so the cost is entered
-- by hand: against a /start campaign or against a `utm_source`, never both.
CREATE TABLE acquisition_spend (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  campaign_id uuid REFERENCES ad_campaigns (id),
  utm_source text,
  spent_on date NOT NULL,
  currency text NOT NULL,
  amount_minor bigint NOT NULL,
  note text NOT NULL DEFAULT '',
  created_by text NOT NULL,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT acquisition_spend_idempotency_key_unique UNIQUE (idempotency_key),
  CONSTRAINT acquisition_spend_idempotency_key_length CHECK (idempotency_key IS NULL OR char_length(idempotency_key) BETWEEN 1 AND 128),
  CONSTRAINT acquisition_spend_one_target CHECK ((campaign_id IS NULL) <> (utm_source IS NULL)),
  CONSTRAINT acquisition_spend_utm_source_shape CHECK (utm_source IS NULL OR utm_source ~ '^[a-z0-9._-]{1,64}$'),
  CONSTRAINT acquisition_spend_currency_shape CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT acquisition_spend_amount_positive CHECK (amount_minor > 0),
  CONSTRAINT acquisition_spend_note_length CHECK (char_length(note) <= 200)
);

CREATE INDEX acquisition_spend_spent_on_idx ON acquisition_spend (spent_on);

-- The first time a customer reached each step of the purchase funnel that no
-- other table records. Sign-up is the customer row, a trial is its claim, and a
-- payment is its order; looking at plans and opening a checkout leave no trace
-- anywhere else. One row per customer and step: the funnel asks whether a
-- customer got that far, not how often.
CREATE TABLE funnel_events (
  user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  step text NOT NULL,
  channel text NOT NULL,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT funnel_events_pkey PRIMARY KEY (user_id, step),
  CONSTRAINT funnel_events_step_known CHECK (step IN ('plans_viewed', 'checkout_started')),
  CONSTRAINT funnel_events_channel_known CHECK (channel IN ('bot', 'web'))
);

-- Customer satisfaction. Once a ticket is resolved the bot asks the customer
-- for a mark from one to five, once; the mark is kept on the ticket.
ALTER TABLE support_tickets
  ADD COLUMN rating smallint,
  ADD COLUMN rated_at timestamptz,
  ADD COLUMN rating_requested_at timestamptz,
  ADD CONSTRAINT support_tickets_rating_range CHECK (rating IS NULL OR rating BETWEEN 1 AND 5),
  ADD CONSTRAINT support_tickets_rated_at_present CHECK ((rating IS NULL) = (rated_at IS NULL));

CREATE INDEX support_tickets_rating_due_idx ON support_tickets (resolved_at)
  WHERE rating_requested_at IS NULL AND rating IS NULL AND status IN ('resolved', 'closed');

CREATE INDEX support_tickets_rated_at_idx ON support_tickets (rated_at) WHERE rating IS NOT NULL;
