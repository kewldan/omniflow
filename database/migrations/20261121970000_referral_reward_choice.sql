-- Bedolaga lets a customer choose whether their referral rewards come as
-- subscription days or as balance, when the programme offers both.
ALTER TABLE referral_programs ADD COLUMN allow_reward_choice boolean NOT NULL DEFAULT false;

CREATE TABLE referral_reward_preferences (
  user_id uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
  kind text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT referral_reward_preferences_kind_known CHECK (kind IN ('wallet', 'days'))
);
