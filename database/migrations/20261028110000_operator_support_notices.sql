-- Operators hear about new support tickets, and the notice-preview topic can be
-- bound at all.
--
-- A customer can open a ticket from the bot, from the bot's free-text fallback,
-- and from the web panel, and each path inserts into `support_tickets` itself.
-- The notice is raised by a trigger rather than by each caller so a fourth path
-- cannot forget it. The payload carries the ticket and queue identifiers only:
-- the subject and the first message are customer text, and operator notices
-- are kept to allowlisted, non-personal fields.
--
-- `notice_test` has been one of the bot's topic kinds since notice previews
-- shipped, but `operator_topics` never allowed it, so binding that topic failed
-- on every start. It is added here beside `support`.
ALTER TABLE operator_topics
  DROP CONSTRAINT operator_topics_kind_known;

ALTER TABLE operator_topics
  ADD CONSTRAINT operator_topics_kind_known CHECK (
    kind IN ('purchase', 'renewal', 'topup', 'refund',
             'fulfillment_failure', 'incident', 'backup', 'security', 'anomaly',
             'campaign_test', 'notice_test', 'support')
  );

ALTER TABLE operator_notifications
  DROP CONSTRAINT operator_notifications_kind_known;

ALTER TABLE operator_notifications
  ADD CONSTRAINT operator_notifications_kind_known CHECK (
    kind IN ('purchase', 'renewal', 'topup', 'refund',
             'fulfillment_failure', 'incident', 'backup', 'security', 'anomaly',
             'support')
  );

CREATE FUNCTION queue_support_ticket_notice() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO operator_notifications (kind, dedupe_key, payload)
  VALUES ('support', 'ticket:' || NEW.id::text,
          jsonb_build_object('ticketId', NEW.id::text, 'queueId', NEW.queue_id::text))
  ON CONFLICT (kind, dedupe_key) DO NOTHING;
  RETURN NEW;
END;
$$;

CREATE TRIGGER support_tickets_queue_notice
  AFTER INSERT ON support_tickets
  FOR EACH ROW EXECUTE FUNCTION queue_support_ticket_notice();
