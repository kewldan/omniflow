-- Two trial rules an operator moving from Bedolaga expects
-- (TRIAL_DISABLED_FOR, TRIAL_WARNING_HOURS).
ALTER TABLE subscription_policy
  -- The sign-in methods that do not qualify for a trial. A customer whose
  -- every way of signing in is on this list is refused one: an account that
  -- also signs in with Telegram still qualifies when only e-mail is listed.
  ADD COLUMN trial_disabled_methods text[] NOT NULL DEFAULT '{}',
  -- How many hours before a trial ends the customer is told so, in addition
  -- to the day-based reminders. Zero sends nothing extra.
  ADD COLUMN trial_warning_hours integer NOT NULL DEFAULT 0,
  ADD CONSTRAINT subscription_policy_trial_methods_known
    CHECK (trial_disabled_methods <@ ARRAY['telegram', 'email', 'oidc']::text[]),
  ADD CONSTRAINT subscription_policy_trial_warning_hours_range
    CHECK (trial_warning_hours BETWEEN 0 AND 168);
