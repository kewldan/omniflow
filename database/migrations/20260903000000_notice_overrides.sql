-- Transactional notices an operator can reword.
--
-- `message_templates` already covers campaigns: an operator writes the body, a
-- campaign sends it. The messages the installation sends on its own initiative
-- — expiry, traffic, renewal, grace, recovery, fulfillment, dunning — are
-- compiled in, so the one voice every customer hears repeatedly is the one
-- nobody can change.
--
-- This is an override table rather than a template table. A row is the
-- exception; its absence is the shipped wording. That direction matters: an
-- installation that has never opened this screen has no rows, upgrades keep
-- getting improved defaults, and deleting a row is a genuine revert rather than
-- a copy of whatever the default happened to be on the day somebody clicked.
--
-- The set of codes and the variables each carries live in `internal/notice`,
-- not here. A CHECK constraint listing them would be a second place that
-- decides what is overridable, and it would need a migration every time a
-- notice is added.

CREATE TABLE notice_overrides (
  code       text NOT NULL,
  locale     text NOT NULL,
  body       text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid REFERENCES admin_users(id),
  PRIMARY KEY (code, locale),
  CONSTRAINT notice_overrides_code_shape CHECK (code ~ '^[a-z][a-z0-9_]{1,63}$'),
  CONSTRAINT notice_overrides_locale_known CHECK (locale IN ('en', 'ru')),
  -- The upper bound is half of Telegram's 4096, because a notice is prefixed
  -- with a subscription line and carries a keyboard. The application refuses a
  -- longer body with a message an operator can act on; this is the floor under
  -- that, so a direct write cannot queue a message that fails at delivery.
  CONSTRAINT notice_overrides_body_length CHECK (char_length(body) BETWEEN 1 AND 2000)
);

COMMENT ON TABLE notice_overrides IS
  'Operator wording for transactional notices. A missing row means the shipped default, so deleting one is a real revert.';

-- A test send, into the operator group.
--
-- A preview renders in the browser and is worth having, but it is a browser
-- rendering Telegram HTML — it cannot show that Telegram accepts the markup,
-- what the emoji look like on a phone, or where the line breaks fall. Sending
-- one copy into the operator group can.
--
-- The rendered body is stored rather than the code and the locale. What the
-- operator asked to see is the text in the editor at that moment, which may not
-- be saved yet and may be edited again before the group receives it. Resolving
-- it at delivery time would show the group something nobody requested.
--
-- It goes to the operator group and never to a customer. A transactional notice
-- has a trigger — a subscription about to expire, a charge that failed — and
-- manufacturing one against a real customer to see how it reads would be a lie
-- told to somebody who is entitled to believe their subscription is in trouble.

CREATE TABLE notice_test_sends (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  code         text NOT NULL,
  locale       text NOT NULL,
  body         text NOT NULL,
  status       text NOT NULL DEFAULT 'pending',
  error_code   text,
  requested_at timestamptz NOT NULL DEFAULT now(),
  resolved_at  timestamptz,
  requested_by uuid REFERENCES admin_users(id),
  CONSTRAINT notice_test_sends_locale_known CHECK (locale IN ('en', 'ru')),
  CONSTRAINT notice_test_sends_status_known CHECK (status IN ('pending', 'sent', 'failed')),
  CONSTRAINT notice_test_sends_body_length CHECK (char_length(body) BETWEEN 1 AND 2000),
  CONSTRAINT notice_test_sends_resolution CHECK ((status = 'pending') = (resolved_at IS NULL))
);

CREATE INDEX notice_test_sends_pending_idx ON notice_test_sends (requested_at)
  WHERE status = 'pending';

CREATE INDEX notice_test_sends_recent_idx ON notice_test_sends (code, requested_at DESC);

COMMENT ON TABLE notice_test_sends IS
  'One rendered copy of a transactional notice, queued for the operator group. Never sent to a customer.';
