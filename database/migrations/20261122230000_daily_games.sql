-- Daily games (Bedolaga's contests): seven kinds a customer plays from the
-- bot or the web panel, each with a prize an operator sets.
--
--   dice            roll a die with crypto/rand; win at or above `parameter` (1–6)
--   slots           three reels of six symbols; win on three of a kind
--   guess_number    pick 1..`parameter` (2–10); the secret is drawn after the pick
--   lucky_ticket    a six-digit ticket; lucky when both halves' digits sum alike
--   quiz            a multiple-choice question the customer has not answered yet
--   daily_streak    a daily check-in; every `parameter`-th day in a row wins (2–30)
--   first_to_answer the first `parameter` correct answers to a round win (1–100)
--
-- Every play is a row, which is the audit. A prize is paid in the play's own
-- transaction through the ledger, a subscription extension, or a personal
-- offer, keyed on the play so a retry cannot pay twice. Limits: plays per
-- customer per UTC day, a daily budget and a daily number of prizes per game
-- (a game that cannot pay its prize today is closed before anyone plays), an
-- optional entry fee with a target return, and the engagement minimum
-- account age.
CREATE TABLE daily_games (
  code text PRIMARY KEY,
  enabled boolean NOT NULL DEFAULT false,
  prize_kind text NOT NULL DEFAULT 'wallet',
  -- Minor units for wallet, days for days, zero for promo.
  prize_value bigint NOT NULL DEFAULT 1000,
  -- The offer template a promo prize sends as a personal offer.
  prize_template_id uuid REFERENCES offer_templates (id),
  plays_per_day integer NOT NULL DEFAULT 1,
  parameter integer NOT NULL DEFAULT 0,
  -- An entry fee charged to the wallet per play; zero means free.
  cost_minor bigint NOT NULL DEFAULT 0,
  target_rtp_percent integer NOT NULL DEFAULT 80,
  -- What the game may pay out per UTC day, prizes valued in minor units
  -- (a day at engagement_settings.day_value_minor); zero means no limit.
  daily_budget_minor bigint NOT NULL DEFAULT 0,
  -- How many prizes the game may pay per UTC day; zero means no limit.
  daily_prize_limit integer NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid REFERENCES admin_users (id) ON DELETE SET NULL,
  CONSTRAINT daily_games_code_known CHECK (code IN (
    'dice', 'slots', 'guess_number', 'lucky_ticket', 'quiz', 'daily_streak', 'first_to_answer'
  )),
  CONSTRAINT daily_games_prize_known CHECK (prize_kind IN ('wallet', 'days', 'promo')),
  CONSTRAINT daily_games_prize_matches_kind CHECK (
    (prize_kind = 'wallet' AND prize_value > 0 AND prize_template_id IS NULL)
    OR (prize_kind = 'days' AND prize_value BETWEEN 1 AND 365 AND prize_template_id IS NULL)
    OR (prize_kind = 'promo' AND prize_value = 0 AND prize_template_id IS NOT NULL)
  ),
  CONSTRAINT daily_games_limits CHECK (
    plays_per_day BETWEEN 1 AND 20 AND cost_minor >= 0 AND target_rtp_percent BETWEEN 1 AND 100
    AND daily_budget_minor >= 0 AND daily_prize_limit >= 0
  ),
  CONSTRAINT daily_games_parameter_fits CHECK (
    CASE code
      WHEN 'dice' THEN parameter BETWEEN 1 AND 6
      WHEN 'guess_number' THEN parameter BETWEEN 2 AND 10
      WHEN 'daily_streak' THEN parameter BETWEEN 2 AND 30
      WHEN 'first_to_answer' THEN parameter BETWEEN 1 AND 100
      ELSE parameter = 0
    END
  ),
  -- A game with one answer or one check-in a day is played once a day.
  CONSTRAINT daily_games_once_a_day CHECK (
    code NOT IN ('quiz', 'daily_streak', 'first_to_answer') OR plays_per_day = 1
  )
);

INSERT INTO daily_games (code, parameter) VALUES
  ('dice', 6), ('slots', 0), ('guess_number', 10), ('lucky_ticket', 0),
  ('quiz', 0), ('daily_streak', 7), ('first_to_answer', 3);

-- Questions for the quiz and for first-to-answer rounds.
CREATE TABLE game_questions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  game_code text NOT NULL REFERENCES daily_games (code),
  question_ru text NOT NULL,
  question_en text NOT NULL,
  -- [{"ru": "…", "en": "…"}, …]
  options jsonb NOT NULL,
  correct_index integer NOT NULL,
  -- A first-to-answer round is open for a day from this moment; a quiz
  -- question is asked from it on.
  opens_at timestamptz NOT NULL DEFAULT now(),
  archived_at timestamptz,
  created_by uuid REFERENCES admin_users (id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT game_questions_game_known CHECK (game_code IN ('quiz', 'first_to_answer')),
  CONSTRAINT game_questions_text_length CHECK (
    char_length(question_ru) BETWEEN 1 AND 300 AND char_length(question_en) BETWEEN 1 AND 300
  ),
  CONSTRAINT game_questions_options_shape CHECK (
    jsonb_typeof(options) = 'array' AND jsonb_array_length(options) BETWEEN 2 AND 6
  ),
  CONSTRAINT game_questions_correct_in_range CHECK (
    correct_index >= 0 AND correct_index < jsonb_array_length(options)
  )
);

CREATE INDEX game_questions_game_opens ON game_questions (game_code, opens_at DESC) WHERE archived_at IS NULL;

CREATE TABLE game_plays (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users (id),
  game_code text NOT NULL REFERENCES daily_games (code),
  play_day date NOT NULL,
  question_id uuid REFERENCES game_questions (id),
  choice integer,
  -- What was drawn or answered: the roll, the reels, the secret, the ticket,
  -- the streak. Kept so an operator can check any result.
  outcome jsonb NOT NULL DEFAULT '{}'::jsonb,
  won boolean NOT NULL,
  prize_kind text,
  prize_value bigint NOT NULL DEFAULT 0,
  -- The prize valued in minor units, for the daily budget.
  prize_value_minor bigint NOT NULL DEFAULT 0,
  personal_offer_id uuid REFERENCES personal_offers (id),
  cost_minor bigint NOT NULL DEFAULT 0,
  currency text NOT NULL,
  idempotency_key text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT game_plays_idempotency_unique UNIQUE (user_id, idempotency_key),
  CONSTRAINT game_plays_prize_known CHECK (prize_kind IS NULL OR prize_kind IN ('wallet', 'days', 'promo')),
  CONSTRAINT game_plays_prize_when_won CHECK (won = (prize_kind IS NOT NULL)),
  CONSTRAINT game_plays_amounts_nonnegative CHECK (prize_value >= 0 AND prize_value_minor >= 0 AND cost_minor >= 0),
  CONSTRAINT game_plays_currency_iso CHECK (currency ~ '^[A-Z]{3}$')
);

CREATE INDEX game_plays_user_game_day ON game_plays (user_id, game_code, play_day);
CREATE INDEX game_plays_game_day_won ON game_plays (game_code, play_day) WHERE won;
-- One answer per customer per question.
CREATE UNIQUE INDEX game_plays_one_answer ON game_plays (user_id, question_id) WHERE question_id IS NOT NULL;
