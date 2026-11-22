-- Forum topics carry their emoji as an icon and a plain name. needs_repair
-- marks a bound topic the bot should rename and give its icon in place
-- (editForumTopic), which the panel's "repair topics" asks for; the bot clears
-- it once the edit went through. A topic it creates is already right.
ALTER TABLE operator_topics ADD COLUMN needs_repair boolean NOT NULL DEFAULT false;

-- The bot's startup report is one status message in the operator group,
-- edited on every start instead of posted again, so an autodeploy that
-- restarts the bot after each commit adds nothing to the chat. One row per
-- group: the message and the topic it is in.
CREATE TABLE operator_status_messages (
    chat_id bigint NOT NULL,
    topic_id bigint NOT NULL DEFAULT 0,
    message_id bigint NOT NULL,
    version text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT operator_status_messages_pkey PRIMARY KEY (chat_id),
    CONSTRAINT operator_status_messages_message_id_check CHECK (message_id > 0),
    CONSTRAINT operator_status_messages_topic_id_check CHECK (topic_id >= 0)
);
