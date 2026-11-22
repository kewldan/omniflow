-- Unit economics, activation, and why customers leave.
--
-- Four kinds of record the business reports could not be built without:
-- what running the service costs, what payment providers keep, whether a
-- customer ever connected, and the reason a customer gives for leaving.

-- What it costs to run the service, entered by an operator: a server bill, a
-- node's rent, software, staff. A cost covers a span of days and is spread
-- evenly over them, so a monthly bill entered once reads correctly in a report
-- of one week. A cost tied to one node lets the report price that node's
-- traffic; one without a node is an overhead of the whole service.
CREATE TABLE operating_costs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  category text NOT NULL,
  node_uuid text,
  covers_from date NOT NULL,
  covers_to date NOT NULL,
  currency text NOT NULL,
  amount_minor bigint NOT NULL,
  note text NOT NULL DEFAULT '',
  created_by text NOT NULL,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT operating_costs_idempotency_key_unique UNIQUE (idempotency_key),
  CONSTRAINT operating_costs_idempotency_key_length CHECK (idempotency_key IS NULL OR char_length(idempotency_key) BETWEEN 1 AND 128),
  CONSTRAINT operating_costs_category_known CHECK (category IN ('servers', 'software', 'staff', 'other')),
  CONSTRAINT operating_costs_node_shape CHECK (node_uuid IS NULL OR node_uuid ~ '^[0-9a-f-]{36}$'),
  CONSTRAINT operating_costs_span_ordered CHECK (covers_to >= covers_from),
  CONSTRAINT operating_costs_span_bounded CHECK (covers_to - covers_from <= 731),
  CONSTRAINT operating_costs_currency_shape CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT operating_costs_amount_positive CHECK (amount_minor > 0),
  CONSTRAINT operating_costs_note_length CHECK (char_length(note) <= 200)
);

CREATE INDEX operating_costs_span_idx ON operating_costs (covers_from, covers_to);

-- What a payment provider keeps of each payment, as the operator's contract
-- states it: a share in basis points and an optional fixed amount per payment
-- in one currency. A rate applies from its date until a later one replaces it,
-- so changing a contract never rewrites last month's margin. Providers do not
-- report their fee in a common shape, which is why the rate is entered rather
-- than read, and why the report calls the result an estimate.
CREATE TABLE payment_provider_fees (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  provider text NOT NULL,
  effective_from date NOT NULL,
  percent_bp integer NOT NULL DEFAULT 0,
  fixed_minor bigint NOT NULL DEFAULT 0,
  fixed_currency text,
  note text NOT NULL DEFAULT '',
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT payment_provider_fees_one_per_day UNIQUE (provider, effective_from),
  CONSTRAINT payment_provider_fees_provider_shape CHECK (provider ~ '^[a-z0-9_]{1,32}$'),
  CONSTRAINT payment_provider_fees_percent_range CHECK (percent_bp BETWEEN 0 AND 10000),
  CONSTRAINT payment_provider_fees_fixed_range CHECK (fixed_minor >= 0),
  CONSTRAINT payment_provider_fees_fixed_currency CHECK (
    (fixed_minor = 0 AND fixed_currency IS NULL)
    OR (fixed_minor > 0 AND fixed_currency ~ '^[A-Z]{3}$')
  ),
  CONSTRAINT payment_provider_fees_note_length CHECK (char_length(note) <= 200)
);

-- What Remnawave last reported about one of its users' connections: when the
-- user first connected, when they were last online, the node they last used,
-- and how many devices they hold against their limit. It is an observation
-- for reporting, like the node traffic samples, and decides nothing; Remnawave
-- stays authoritative. first_connected_at is kept once seen, so a period
-- whose Remnawave user was later deleted still says whether it was used.
CREATE TABLE vpn_usage_observations (
  remnawave_user_id bigint PRIMARY KEY,
  first_connected_at timestamptz,
  online_at timestamptz,
  last_node_uuid text,
  active_devices integer NOT NULL DEFAULT 0,
  device_limit integer,
  observed_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT vpn_usage_observations_remnawave_user_id_positive CHECK (remnawave_user_id > 0),
  CONSTRAINT vpn_usage_observations_devices_range CHECK (active_devices >= 0),
  CONSTRAINT vpn_usage_observations_device_limit_range CHECK (device_limit IS NULL OR device_limit >= 0)
);

CREATE INDEX vpn_usage_observations_last_node_idx ON vpn_usage_observations (last_node_uuid);

-- The reason a customer gives for leaving: when they switch automatic renewal
-- off, or when they answer the message that their subscription expired. One
-- answer per customer, subscription, occasion, and day; answering again the
-- same day replaces it. A closed list rather than free text, so nothing a
-- customer types is kept and the answers can be counted.
CREATE TABLE churn_feedback (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  subscription_id uuid REFERENCES subscriptions (id) ON DELETE SET NULL,
  occasion text NOT NULL,
  reason text NOT NULL,
  surface text NOT NULL,
  answered_on date NOT NULL DEFAULT (now() AT TIME ZONE 'UTC')::date,
  answered_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT churn_feedback_occasion_known CHECK (occasion IN ('auto_renew_off', 'lapsed')),
  CONSTRAINT churn_feedback_reason_known CHECK (
    reason IN ('price', 'blocked', 'speed', 'not_needed', 'switched', 'technical', 'other')
  ),
  CONSTRAINT churn_feedback_surface_known CHECK (surface IN ('bot', 'web'))
);

CREATE UNIQUE INDEX churn_feedback_one_answer_idx ON churn_feedback (
  user_id, occasion, answered_on, COALESCE(subscription_id, '00000000-0000-0000-0000-000000000000'::uuid)
);
CREATE INDEX churn_feedback_answered_at_idx ON churn_feedback (answered_at);
