-- Operator-built reminders: a message sent again and again to whoever is in
-- a segment — no subscription, ending soon, ended, low wallet balance — until
-- they leave it or the reminder has been sent its number of times. Bedolaga
-- calls this its reminder constructor. A reminder can also show as a card in
-- the customer's web dashboard, which the customer can dismiss.
CREATE TABLE reminders (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    name text NOT NULL,
    active boolean DEFAULT false NOT NULL,
    category text DEFAULT 'service'::text NOT NULL,
    segment text NOT NULL,
    segment_days integer DEFAULT 3 NOT NULL,
    balance_below_minor bigint DEFAULT 0 NOT NULL,
    currency text DEFAULT 'RUB'::text NOT NULL,
    repeat_every_days integer DEFAULT 7 NOT NULL,
    max_sends integer DEFAULT 1 NOT NULL,
    title_ru text NOT NULL,
    body_ru text NOT NULL,
    title_en text NOT NULL,
    body_en text NOT NULL,
    button_route text,
    show_in_cabinet boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by uuid,
    CONSTRAINT reminders_name_length CHECK (char_length(name) BETWEEN 1 AND 80),
    CONSTRAINT reminders_category_known CHECK (category = ANY (ARRAY['service'::text, 'marketing'::text])),
    CONSTRAINT reminders_segment_known CHECK (segment = ANY (ARRAY['no_subscription'::text, 'expiring'::text, 'expired'::text, 'low_balance'::text])),
    CONSTRAINT reminders_segment_days_range CHECK (segment_days BETWEEN 0 AND 365),
    CONSTRAINT reminders_balance_nonnegative CHECK (balance_below_minor >= 0),
    CONSTRAINT reminders_currency_shape CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT reminders_repeat_range CHECK (repeat_every_days BETWEEN 1 AND 365),
    CONSTRAINT reminders_sends_range CHECK (max_sends BETWEEN 1 AND 20),
    CONSTRAINT reminders_titles_length CHECK (char_length(title_ru) BETWEEN 1 AND 120 AND char_length(title_en) BETWEEN 1 AND 120),
    CONSTRAINT reminders_bodies_length CHECK (char_length(body_ru) BETWEEN 1 AND 2000 AND char_length(body_en) BETWEEN 1 AND 2000),
    CONSTRAINT reminders_button_known CHECK (button_route IS NULL OR button_route = ANY (ARRAY['plans'::text, 'connect'::text, 'wallet'::text, 'support'::text, 'home'::text]))
);

-- How far one customer is through one reminder. Each delivery increments it;
-- a dismissed cabinet card stops both the card and the messages.
CREATE TABLE reminder_states (
    reminder_id uuid NOT NULL REFERENCES reminders (id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    sends integer DEFAULT 0 NOT NULL,
    last_sent_at timestamp with time zone,
    dismissed_at timestamp with time zone,
    PRIMARY KEY (reminder_id, user_id),
    CONSTRAINT reminder_states_sends_nonnegative CHECK (sends >= 0)
);

CREATE INDEX reminder_states_recent_idx ON reminder_states (user_id, last_sent_at DESC);

-- Whether one customer is inside one reminder's segment now. The delivery
-- pass and the cabinet cards both ask it, so the two cannot disagree about who
-- a reminder is for.
CREATE FUNCTION reminder_segment_holds(target_reminder uuid, target_user uuid) RETURNS boolean
    LANGUAGE sql STABLE
    AS $$
  WITH reminder AS (SELECT * FROM reminders WHERE id = target_reminder),
  live AS (
    SELECT max(ends_at) AS ends_at, count(*) AS entitlements FROM entitlements
    WHERE user_id = target_user AND status IN ('active', 'limited', 'paused')
  ),
  lapsed AS (
    SELECT max(ends_at) AS ends_at, count(*) AS entitlements FROM entitlements
    WHERE user_id = target_user AND status IN ('active', 'limited', 'disabled', 'expired')
  ),
  balance AS (
    SELECT COALESCE(sum(l.amount_minor), 0)::bigint AS minor
    FROM ledger_entries l, reminder
    WHERE l.account_type = 'customer_wallet' AND l.user_id = target_user AND l.currency = reminder.currency
  )
  SELECT COALESCE((
    SELECT CASE reminder.segment
      WHEN 'no_subscription' THEN live.entitlements = 0 AND lapsed.entitlements = 0
      WHEN 'expiring' THEN live.ends_at > now() AND live.ends_at <= now() + make_interval(days => reminder.segment_days)
      WHEN 'expired' THEN (live.entitlements = 0 OR live.ends_at <= now())
        AND lapsed.ends_at <= now() - make_interval(days => reminder.segment_days)
      WHEN 'low_balance' THEN balance.minor < reminder.balance_below_minor
      ELSE false
    END
    FROM reminder, live, lapsed, balance
  ), false)
$$;
