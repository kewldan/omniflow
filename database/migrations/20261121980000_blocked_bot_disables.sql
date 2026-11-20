-- Bedolaga can switch off the subscriptions of users who blocked the bot.
-- With Settings → Marketing → disableBlockedSubscriptions on, the worker
-- disables a live subscription whose owner has blocked the bot for longer than
-- the grace period, records it here, and enables it again once the customer
-- talks to the bot.
CREATE TABLE blocked_bot_disables (
  entitlement_id uuid PRIMARY KEY REFERENCES entitlements (id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  disabled_at timestamptz NOT NULL DEFAULT now(),
  restored_at timestamptz
);
CREATE INDEX blocked_bot_disables_open_idx ON blocked_bot_disables (user_id) WHERE restored_at IS NULL;
