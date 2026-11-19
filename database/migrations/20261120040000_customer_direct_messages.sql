-- Messages an operator writes to one customer from the panel, delivered by the
-- bot. The body is kept so support can see what was said; the audit log
-- records who wrote it and why.
CREATE TABLE customer_direct_messages (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  body text NOT NULL,
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  status text NOT NULL DEFAULT 'pending',
  sent_at timestamptz,
  error_code text,
  CONSTRAINT customer_direct_messages_body_length CHECK (char_length(body) BETWEEN 1 AND 3500),
  CONSTRAINT customer_direct_messages_status_known CHECK (status IN ('pending', 'sent', 'failed'))
);
CREATE INDEX customer_direct_messages_pending ON customer_direct_messages (created_at) WHERE status = 'pending';
CREATE INDEX customer_direct_messages_user ON customer_direct_messages (user_id, created_at DESC);
