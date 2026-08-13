-- A test send: one copy of a campaign's message, to the operators, before the
-- audience is committed to.
--
-- It is a separate table from `campaign_recipients` rather than a flag on one,
-- and that separation is the whole point. Every counter an operator reads —
-- queued, sent, failed, suppressed — is derived from the recipients table, and
-- the audience expansion in `internal/campaigns` refuses to queue a customer who
-- already has a row there. A test send recorded as a recipient would move the
-- counters an operator is about to judge the campaign by, and would remove
-- somebody from the real audience. Neither is recoverable once the campaign has
-- run.
--
-- Nothing here is a recipient in the product's sense either: the message goes to
-- the operator group, which is a chat the installation owns, not to a customer.
-- So there is no user_id, no suppression reason, and no delivery policy — none
-- of the three applies to a message an operator asked to be shown.
CREATE TABLE campaign_test_sends (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  campaign_id uuid NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,

  -- Which language to render. The template holds both, and the operator
  -- reviewing a Russian campaign needs to see the Russian copy rather than
  -- whichever one a default picked.
  locale text NOT NULL CHECK (locale IN ('en', 'ru')),

  status text NOT NULL DEFAULT 'queued' CHECK (
    status IN ('queued', 'sent', 'failed')
  ),
  -- A category, never Telegram's own message: it can quote the chat and the
  -- bot token's owner, and this row is readable by anyone who can read the
  -- campaign.
  error_code text CHECK (error_code IS NULL OR char_length(error_code) <= 60),

  -- Who asked. A test send costs nothing and reveals the campaign copy to the
  -- operator group, so "who put this in the chat?" should have an answer that
  -- does not depend on the audit trail still being readable.
  requested_by uuid REFERENCES admin_users(id),
  requested_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz,

  -- A resolved row has an outcome and an outcome has a time. The pairing is a
  -- constraint rather than a convention because the delivery loop is the only
  -- writer and a half-written row would look like one still in flight.
  CONSTRAINT campaign_test_sends_resolution_shape
    CHECK ((status = 'queued') = (resolved_at IS NULL))
);

-- The delivery loop reads the queue in order and nothing else does.
CREATE INDEX campaign_test_sends_pending_idx
  ON campaign_test_sends (requested_at)
  WHERE status = 'queued';

CREATE INDEX campaign_test_sends_campaign_idx
  ON campaign_test_sends (campaign_id, requested_at DESC);

-- A forum topic of its own for the tests.
--
-- Only `operator_topics` gains the kind. `operator_notifications` deliberately
-- does not: a notice there is a set of allowlisted, non-personal fields that
-- `renderNotification` prints, and a campaign test is a rendered message body.
-- Putting one through that table would mean either a column for content in a
-- table designed to have none, or a renderer that prints whatever it is handed.
-- The queue for tests is `campaign_test_sends` above.
ALTER TABLE operator_topics
  DROP CONSTRAINT operator_topics_kind_known;

ALTER TABLE operator_topics
  ADD CONSTRAINT operator_topics_kind_known CHECK (
    kind IN ('purchase', 'renewal', 'topup', 'refund',
             'fulfillment_failure', 'incident', 'backup', 'security', 'anomaly',
             'campaign_test')
  );
