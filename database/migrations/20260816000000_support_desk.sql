-- The support desk: queues, assignment, tags, internal notes, canned replies,
-- merges, and the timestamps that make response time measurable.
--
-- v0.4 gave a customer one open ticket and an operator a Telegram topic to
-- answer it in. That is enough for one person answering a handful of tickets a
-- day and stops being enough the moment there are two operators, because
-- nothing says who is answering what.

-- ---------------------------------------------------------------------------
-- Queues
-- ---------------------------------------------------------------------------

-- A queue is a named bucket with a target response time.
--
-- The target is stored on the queue rather than derived from the priority
-- because they answer different questions: priority is how this ticket compares
-- to the others in front of it, and the target is what the operator promised
-- the customer. A billing queue may promise four hours for everything in it
-- while a general queue promises two days.
CREATE TABLE support_queues (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  code text NOT NULL UNIQUE CHECK (code ~ '^[a-z][a-z0-9_-]{1,63}$'),
  name_en text NOT NULL CHECK (char_length(name_en) BETWEEN 1 AND 80),
  name_ru text NOT NULL CHECK (char_length(name_ru) BETWEEN 1 AND 80),

  -- How long a ticket in this queue should wait for its first reply, and for
  -- resolution. Zero means the queue makes no promise, which is honest for a
  -- catch-all.
  first_response_target_seconds bigint NOT NULL DEFAULT 0
    CHECK (first_response_target_seconds >= 0),
  resolution_target_seconds bigint NOT NULL DEFAULT 0
    CHECK (resolution_target_seconds >= 0),

  -- Exactly one queue receives tickets that name no queue. The partial unique
  -- index below enforces it, so an installation can never be in a state where
  -- a new ticket has nowhere to go.
  is_default boolean NOT NULL DEFAULT false,
  sort_order integer NOT NULL DEFAULT 0,
  archived_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX support_queues_one_default_idx
  ON support_queues (is_default) WHERE is_default AND archived_at IS NULL;

-- The catch-all every installation starts with. An operator can rename it,
-- add queues beside it, or move the default elsewhere, but there is always one.
INSERT INTO support_queues (code, name_en, name_ru, is_default, sort_order)
VALUES ('general', 'General', 'Общие вопросы', true, 0);

-- ---------------------------------------------------------------------------
-- Tickets
-- ---------------------------------------------------------------------------

-- The one-open-ticket-per-customer rule goes.
--
-- It was a reasonable simplification when a customer had one conversation
-- thread in a bot. It is wrong for a desk: a customer with a billing question
-- and a connection problem has two tickets, and forcing them into one thread
-- makes both harder to answer and impossible to route separately.
DROP INDEX support_tickets_one_open_per_user_idx;

ALTER TABLE support_tickets
  ADD COLUMN queue_id uuid REFERENCES support_queues(id),
  ADD COLUMN assignee_id uuid REFERENCES admin_users(id),

  -- Assignment history in miniature: who took it and when. A ticket that has
  -- been picked up and put down repeatedly is a ticket nobody owns, and the
  -- audit trail carries the detail.
  ADD COLUMN assigned_at timestamptz,

  -- SLA timestamps. These are recorded facts rather than computed views,
  -- because "when was this first answered" must survive a queue's target being
  -- changed afterwards.
  ADD COLUMN first_response_at timestamptz,
  ADD COLUMN resolved_at timestamptz,
  ADD COLUMN reopened_count integer NOT NULL DEFAULT 0 CHECK (reopened_count >= 0),

  -- Operator-side unread. The customer's counter already exists; this is its
  -- mirror, so a queue can show what has arrived since anybody looked.
  ADD COLUMN operator_unread_count integer NOT NULL DEFAULT 0
    CHECK (operator_unread_count >= 0),

  -- A merged ticket keeps its row and points at its survivor. Deleting it
  -- would lose the customer's own words and the audit trail that explains
  -- where they went.
  ADD COLUMN merged_into_ticket_id uuid REFERENCES support_tickets(id),

  ADD CONSTRAINT support_tickets_merge_is_not_self
    CHECK (merged_into_ticket_id IS NULL OR merged_into_ticket_id <> id);

-- Status gains the two states a desk needs between open and closed.
ALTER TABLE support_tickets
  DROP CONSTRAINT support_tickets_status_check;

ALTER TABLE support_tickets
  ADD CONSTRAINT support_tickets_status_check CHECK (
    status IN ('open', 'pending', 'resolved', 'closed', 'merged')
  );

UPDATE support_tickets
SET queue_id = (SELECT id FROM support_queues WHERE code = 'general');

ALTER TABLE support_tickets ALTER COLUMN queue_id SET NOT NULL;

-- The queue view: open work, oldest first, by queue and assignee.
CREATE INDEX support_tickets_queue_idx
  ON support_tickets (queue_id, status, last_message_at)
  WHERE status IN ('open', 'pending');
CREATE INDEX support_tickets_assignee_idx
  ON support_tickets (assignee_id, status, last_message_at)
  WHERE assignee_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Tags
-- ---------------------------------------------------------------------------

CREATE TABLE support_tags (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  code text NOT NULL UNIQUE CHECK (code ~ '^[a-z][a-z0-9_-]{1,63}$'),
  name_en text NOT NULL CHECK (char_length(name_en) BETWEEN 1 AND 60),
  name_ru text NOT NULL CHECK (char_length(name_ru) BETWEEN 1 AND 60),
  archived_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE support_ticket_tags (
  ticket_id uuid NOT NULL REFERENCES support_tickets(id) ON DELETE CASCADE,
  tag_id uuid NOT NULL REFERENCES support_tags(id) ON DELETE CASCADE,
  tagged_by uuid REFERENCES admin_users(id),
  tagged_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (ticket_id, tag_id)
);

CREATE INDEX support_ticket_tags_tag_idx ON support_ticket_tags (tag_id);

-- ---------------------------------------------------------------------------
-- Internal notes
-- ---------------------------------------------------------------------------

-- A note is not a message.
--
-- It is a separate table rather than a `visibility` column on support_messages,
-- and that is deliberate. Every path that delivers a message to a customer
-- reads support_messages; a flag on that table would mean every one of those
-- paths has to remember to check it, and the first one that forgets sends an
-- operator's private note to the customer it is about. A note cannot be
-- delivered because it is not in the table the delivery path reads.
CREATE TABLE support_notes (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  ticket_id uuid NOT NULL REFERENCES support_tickets(id) ON DELETE CASCADE,
  author_id uuid NOT NULL REFERENCES admin_users(id),
  body text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 4000),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX support_notes_ticket_idx ON support_notes (ticket_id, created_at);

-- ---------------------------------------------------------------------------
-- Canned responses
-- ---------------------------------------------------------------------------

-- Both languages are required. A canned reply that exists in one language is a
-- reply an operator will send to the wrong half of the customer base, because
-- the language of the customer is not the language of the operator.
CREATE TABLE support_canned_responses (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  code text NOT NULL UNIQUE CHECK (code ~ '^[a-z][a-z0-9_-]{1,63}$'),
  title_en text NOT NULL CHECK (char_length(title_en) BETWEEN 1 AND 80),
  title_ru text NOT NULL CHECK (char_length(title_ru) BETWEEN 1 AND 80),
  body_en text NOT NULL CHECK (char_length(body_en) BETWEEN 1 AND 4000),
  body_ru text NOT NULL CHECK (char_length(body_ru) BETWEEN 1 AND 4000),

  -- A canned response an operator may insert but not edit is the common case:
  -- refund wording and policy statements are written once and reused, and an
  -- operator changing them in the moment is how two customers get told
  -- different things.
  requires_permission text NOT NULL DEFAULT 'support.write'
    CHECK (char_length(requires_permission) BETWEEN 3 AND 64),

  usage_count bigint NOT NULL DEFAULT 0 CHECK (usage_count >= 0),
  archived_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid REFERENCES admin_users(id)
);

CREATE INDEX support_canned_responses_active_idx
  ON support_canned_responses (code) WHERE archived_at IS NULL;

-- ---------------------------------------------------------------------------
-- Operator authorship on messages
-- ---------------------------------------------------------------------------

-- Who wrote an operator reply. v0.4 recorded that a reply came from "operator";
-- with a desk, "which operator" is the question the workload report answers.
ALTER TABLE support_messages
  ADD COLUMN author_id uuid REFERENCES admin_users(id),
  ADD COLUMN canned_response_id uuid REFERENCES support_canned_responses(id);

CREATE INDEX support_messages_author_idx
  ON support_messages (author_id, created_at DESC) WHERE author_id IS NOT NULL;
