-- Partner applications (Bedolaga's partner programme). A customer — a
-- blogger, a channel owner — applies from the bot or the web panel; an
-- operator approves with a personal commission rate or rejects. Approval
-- writes the rate to referral_inviter_terms in the same transaction, so the
-- commission path that already reads personal rates pays the partner rate;
-- nothing here pays money itself.
--
-- An application is never deleted: its decision, rate, and who made it are
-- the record of why this customer earns what they earn.
CREATE TABLE partner_applications (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  status text NOT NULL DEFAULT 'pending',
  source text NOT NULL,
  -- Where the customer will promote the service, and anything else they
  -- want the operator to know. Both are the customer's own words.
  channel text NOT NULL DEFAULT '',
  message text NOT NULL DEFAULT '',
  -- The rate an approval granted, in basis points.
  commission_bps integer,
  first_payment_commission_bps integer,
  decision_note text NOT NULL DEFAULT '',
  decided_by uuid REFERENCES admin_users (id) ON DELETE SET NULL,
  decided_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT partner_applications_status_known CHECK (status IN ('pending', 'approved', 'rejected')),
  CONSTRAINT partner_applications_source_known CHECK (source IN ('bot', 'web')),
  CONSTRAINT partner_applications_text_length CHECK (
    char_length(channel) <= 200 AND char_length(message) <= 1000 AND char_length(decision_note) <= 400
  ),
  CONSTRAINT partner_applications_rates_bounded CHECK (
    (commission_bps IS NULL OR commission_bps BETWEEN 0 AND 10000)
    AND (first_payment_commission_bps IS NULL OR first_payment_commission_bps BETWEEN 0 AND 10000)
  ),
  CONSTRAINT partner_applications_decision_complete CHECK (
    (status = 'pending') = (decided_at IS NULL)
    AND (status <> 'approved' OR commission_bps IS NOT NULL)
  )
);

-- One open application per customer.
CREATE UNIQUE INDEX partner_applications_one_pending ON partner_applications (user_id) WHERE status = 'pending';
CREATE INDEX partner_applications_status_created ON partner_applications (status, created_at DESC);
CREATE INDEX partner_applications_user ON partner_applications (user_id, created_at DESC);
