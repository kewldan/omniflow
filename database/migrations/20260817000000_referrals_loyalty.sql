-- Referral review and loyalty tiers.
--
-- v0.4 built the referral program: attribution, qualification, and a wallet
-- reward through the ledger. What it has no answer for is the case an operator
-- actually has to handle — somebody gaming it — and there is no loyalty model
-- at all.

-- ---------------------------------------------------------------------------
-- Referral review
-- ---------------------------------------------------------------------------

-- An attribution can now be held, rejected, or reinstated by a person.
--
-- `rejected_reason` already existed as free text. What was missing was who
-- decided, when, and whether the decision was automatic or a person's — which
-- is the difference between a system an operator can defend and one they can
-- only apologise for.
ALTER TABLE referral_attributions
  ADD COLUMN review_state text NOT NULL DEFAULT 'clear'
    CHECK (review_state IN ('clear', 'held', 'rejected')),
  ADD COLUMN reviewed_by uuid REFERENCES admin_users(id),
  ADD COLUMN reviewed_at timestamptz,
  ADD COLUMN review_note text CHECK (review_note IS NULL OR char_length(review_note) <= 400),

  -- Why the system thought this pair was worth a look. It is advisory: a signal
  -- holds a reward for review, and never rejects one on its own.
  ADD COLUMN signal_codes text[] NOT NULL DEFAULT '{}';

CREATE INDEX referral_attributions_review_idx
  ON referral_attributions (review_state, created_at DESC)
  WHERE review_state <> 'clear';

-- A reward that was granted and then reversed keeps both records.
--
-- The reversal is a compensating ledger transaction rather than a deletion or a
-- negative edit of the original, because the ledger is append-only and "this
-- was granted and then taken back, by this operator, for this reason" is a
-- different fact from "this was never granted".
ALTER TABLE referral_rewards
  ADD COLUMN reversed_at timestamptz,
  ADD COLUMN reversed_by uuid REFERENCES admin_users(id),
  ADD COLUMN reversal_reason text
    CHECK (reversal_reason IS NULL OR char_length(reversal_reason) <= 400),
  ADD COLUMN reversal_ledger_transaction_id uuid REFERENCES ledger_transactions(id),

  ADD CONSTRAINT referral_rewards_reversal_complete
    CHECK ((reversed_at IS NULL) = (reversal_ledger_transaction_id IS NULL));

-- Abuse signals, recorded rather than acted on.
--
-- Omniflow does not suspend an account because a heuristic fired. A signal
-- holds a referral reward for a person to look at, and the person decides. That
-- is the whole policy: an automatic system that punishes customers silently is
-- one nobody can explain to the customer it punished.
CREATE TABLE referral_signals (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  referred_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code text NOT NULL CHECK (code ~ '^[a-z][a-z0-9_]{2,63}$'),

  -- What the signal saw, in terms an operator can check. It carries counts and
  -- identifiers, never a raw device fingerprint or an IP address: a review
  -- surface is not a reason to widen what is retained.
  evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
  detected_at timestamptz NOT NULL DEFAULT now(),

  UNIQUE (referred_user_id, code)
);

CREATE INDEX referral_signals_recent_idx ON referral_signals (detected_at DESC);

-- ---------------------------------------------------------------------------
-- Loyalty
-- ---------------------------------------------------------------------------

-- A loyalty definition is versioned and immutable once published.
--
-- Tiers decide what a customer is entitled to, and a customer who reached gold
-- under one set of thresholds should not silently fall out of it because an
-- operator edited the numbers. Editing publishes a new version; the old one
-- keeps explaining the assignments made under it.
CREATE TABLE loyalty_programs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  version integer NOT NULL CHECK (version > 0),
  enabled boolean NOT NULL DEFAULT false,

  -- What the tiers are measured on. Spend is the settled amount over the
  -- window; tenure is how long the customer has held an active subscription.
  -- Both are facts Omniflow already records, which is deliberate: a loyalty
  -- model that needs new tracking is a loyalty model that needs new consent.
  metric text NOT NULL DEFAULT 'spend'
    CHECK (metric IN ('spend', 'tenure', 'orders')),
  currency text NOT NULL DEFAULT 'RUB' CHECK (currency ~ '^[A-Z]{3}$'),
  window_days integer NOT NULL DEFAULT 365 CHECK (window_days > 0),

  -- A tier is kept for this long after the customer stops qualifying, so a
  -- quiet month does not demote somebody who has been with the service for a
  -- year. Zero demotes as soon as they fall below the threshold.
  grace_days integer NOT NULL DEFAULT 30 CHECK (grace_days >= 0),

  published_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid REFERENCES admin_users(id),

  UNIQUE (version)
);

CREATE UNIQUE INDEX loyalty_programs_one_enabled_idx
  ON loyalty_programs (enabled) WHERE enabled;

CREATE TABLE loyalty_tiers (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  program_id uuid NOT NULL REFERENCES loyalty_programs(id) ON DELETE CASCADE,
  code text NOT NULL CHECK (code ~ '^[a-z][a-z0-9_-]{1,31}$'),
  name_en text NOT NULL CHECK (char_length(name_en) BETWEEN 1 AND 60),
  name_ru text NOT NULL CHECK (char_length(name_ru) BETWEEN 1 AND 60),

  -- The threshold the metric must reach. Tiers are ordered by it, and the
  -- lowest tier is expected to be zero so every customer has one.
  threshold bigint NOT NULL CHECK (threshold >= 0),

  -- What the tier is worth. A percentage discount is the only reward for now,
  -- and it composes with promotions through the existing stacking rules rather
  -- than through a second discount engine.
  discount_bps integer NOT NULL DEFAULT 0 CHECK (discount_bps BETWEEN 0 AND 10000),

  sort_order integer NOT NULL DEFAULT 0,

  UNIQUE (program_id, code),
  UNIQUE (program_id, threshold)
);

-- One customer's current standing, and how it was arrived at.
--
-- `evaluated_metric` and `program_version` are stored rather than recomputed on
-- read, so an operator answering "why am I silver?" gets the number the
-- decision was actually made on, under the definition that was in force.
CREATE TABLE loyalty_standings (
  user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  program_id uuid NOT NULL REFERENCES loyalty_programs(id),
  tier_id uuid NOT NULL REFERENCES loyalty_tiers(id),
  evaluated_metric bigint NOT NULL CHECK (evaluated_metric >= 0),
  evaluated_at timestamptz NOT NULL DEFAULT now(),

  -- When the tier stops being held on grace. Null means the customer currently
  -- qualifies on their own.
  grace_until timestamptz,

  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX loyalty_standings_tier_idx ON loyalty_standings (tier_id);

-- Every change of standing, kept.
--
-- A customer asking why they were demoted deserves an answer, and an operator
-- correcting a mistake needs to see what the previous state was.
CREATE TABLE loyalty_standing_history (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  from_tier_id uuid REFERENCES loyalty_tiers(id),
  to_tier_id uuid NOT NULL REFERENCES loyalty_tiers(id),
  evaluated_metric bigint NOT NULL,
  reason text NOT NULL CHECK (reason IN ('evaluation', 'operator_override', 'program_change')),
  actor_id uuid REFERENCES admin_users(id),
  occurred_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX loyalty_standing_history_customer_idx
  ON loyalty_standing_history (user_id, occurred_at DESC);
