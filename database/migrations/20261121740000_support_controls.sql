-- Support controls Bedolaga operators are used to: blocking a customer from
-- support, a "payment problem" topic and an urgent flag the customer picks,
-- and removing a message from the customer's view.

-- A support block is a restriction like "no top-ups": one row, with a reason.
ALTER TABLE customer_restrictions DROP CONSTRAINT customer_restrictions_kind_known;
ALTER TABLE customer_restrictions ADD CONSTRAINT customer_restrictions_kind_known
  CHECK (kind = ANY (ARRAY['topup', 'purchase', 'support']::text[]));

-- The database refuses a new ticket or a customer message from a blocked
-- customer, whichever surface writes it, so a forgotten check in one code path
-- cannot let a blocked customer through.
CREATE FUNCTION refuse_blocked_support() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
  customer uuid;
BEGIN
  IF TG_TABLE_NAME = 'support_tickets' THEN
    customer := NEW.user_id;
  ELSIF NEW.sender = 'customer' THEN
    SELECT user_id INTO customer FROM support_tickets WHERE id = NEW.ticket_id;
  ELSE
    RETURN NEW;
  END IF;
  IF EXISTS (SELECT 1 FROM customer_restrictions WHERE user_id = customer AND kind = 'support') THEN
    RAISE EXCEPTION 'support_blocked' USING ERRCODE = 'OFSB1';
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER support_tickets_refuse_blocked BEFORE INSERT ON support_tickets
  FOR EACH ROW EXECUTE FUNCTION refuse_blocked_support();
CREATE TRIGGER support_messages_refuse_blocked BEFORE INSERT ON support_messages
  FOR EACH ROW EXECUTE FUNCTION refuse_blocked_support();

-- Queues a customer may choose when opening a ticket, and the payment one.
ALTER TABLE support_queues ADD COLUMN customer_selectable boolean NOT NULL DEFAULT false;
UPDATE support_queues SET customer_selectable = true WHERE is_default;
INSERT INTO support_queues (code, name_en, name_ru, customer_selectable, sort_order)
VALUES ('payment', 'Payment problem', 'Проблема с оплатой', true, 5)
ON CONFLICT (code) DO NOTHING;

-- The topic and urgency the customer chose for the ticket they are about to
-- open. The next ticket they open takes it, and the choice is consumed.
CREATE TABLE support_topic_choices (
  user_id uuid NOT NULL,
  queue_id uuid,
  priority text NOT NULL DEFAULT 'normal',
  chosen_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT support_topic_choices_pkey PRIMARY KEY (user_id),
  CONSTRAINT support_topic_choices_user_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
  CONSTRAINT support_topic_choices_queue_fkey FOREIGN KEY (queue_id) REFERENCES support_queues(id) ON DELETE SET NULL,
  CONSTRAINT support_topic_choices_priority_known CHECK (priority = ANY (ARRAY['normal', 'high']::text[]))
);

CREATE FUNCTION apply_support_topic_choice() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
  choice support_topic_choices;
BEGIN
  DELETE FROM support_topic_choices
  WHERE user_id = NEW.user_id AND chosen_at > now() - interval '1 hour'
  RETURNING * INTO choice;
  IF choice.user_id IS NOT NULL THEN
    IF choice.queue_id IS NOT NULL AND EXISTS (
      SELECT 1 FROM support_queues q WHERE q.id = choice.queue_id AND q.archived_at IS NULL AND q.customer_selectable
    ) THEN
      NEW.queue_id := choice.queue_id;
    END IF;
    IF choice.priority = 'high' AND NEW.priority = 'normal' THEN
      NEW.priority := 'high';
    END IF;
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER support_tickets_topic_choice BEFORE INSERT ON support_tickets
  FOR EACH ROW EXECUTE FUNCTION apply_support_topic_choice();

-- An operator may take a message out of the customer's view — a wrong reply,
-- a pasted secret. The row stays for the desk and the audit trail.
ALTER TABLE support_messages ADD COLUMN deleted_at timestamptz;
ALTER TABLE support_messages ADD COLUMN deleted_by uuid;
