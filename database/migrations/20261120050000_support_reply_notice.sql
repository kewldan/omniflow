-- Operators heard about a new ticket but not about a customer answering in
-- one they already had, so a reply could wait unseen until someone opened the
-- desk. Every customer message after a ticket's first now queues a support
-- notice too. Like the new-ticket notice it carries identifiers only, never
-- the customer's text.
CREATE FUNCTION queue_support_reply_notice() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF NEW.sender <> 'customer' THEN
    RETURN NEW;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM support_messages earlier
    WHERE earlier.ticket_id = NEW.ticket_id AND earlier.id < NEW.id
  ) THEN
    -- The ticket's opening message: the new-ticket notice already covers it.
    RETURN NEW;
  END IF;
  INSERT INTO operator_notifications (kind, dedupe_key, payload)
  SELECT 'support', 'reply:' || NEW.id::text,
         jsonb_build_object('ticketId', t.id::text, 'queueId', t.queue_id::text, 'event', 'customer_reply')
  FROM support_tickets t WHERE t.id = NEW.ticket_id
  ON CONFLICT (kind, dedupe_key) DO NOTHING;
  RETURN NEW;
END;
$$;

CREATE TRIGGER support_messages_reply_notice AFTER INSERT ON support_messages
  FOR EACH ROW EXECUTE FUNCTION queue_support_reply_notice();
