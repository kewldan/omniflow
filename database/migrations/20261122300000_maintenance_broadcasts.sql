-- Customer notices about maintenance.
--
-- When maintenance is entered or left — by an operator or by automatic
-- detection — and the maintenance settings ask for it, one row is written here
-- in the same transaction as the maintenance event. The bot and the e-mail
-- pass then deliver it to customers through their ordinary delivery policy;
-- each delivery is keyed by this row's id, so a restarted pass never sends a
-- customer the same notice twice. The customer-facing notice and the expected
-- return are copied in, so a notice still going out after the switch moved
-- again says what was true when it was written.
CREATE TABLE maintenance_broadcasts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id uuid NOT NULL REFERENCES maintenance_events (id),
    transition text NOT NULL,
    notice_ru text NOT NULL DEFAULT '',
    notice_en text NOT NULL DEFAULT '',
    expected_return_at timestamptz,
    email boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT maintenance_broadcasts_event_unique UNIQUE (event_id),
    CONSTRAINT maintenance_broadcasts_transition_known CHECK (transition IN ('activated', 'cleared')),
    CONSTRAINT maintenance_broadcasts_notice_ru_length CHECK (char_length(notice_ru) <= 1000),
    CONSTRAINT maintenance_broadcasts_notice_en_length CHECK (char_length(notice_en) <= 1000)
);

CREATE INDEX maintenance_broadcasts_created_at_idx ON maintenance_broadcasts (created_at DESC);

-- The e-mail queue learns the maintenance kind. The full set is restated.
ALTER TABLE email_outbox DROP CONSTRAINT email_outbox_kind_known;
ALTER TABLE email_outbox ADD CONSTRAINT email_outbox_kind_known CHECK (kind IN (
    'receipt', 'campaign', 'offer', 'guest_purchase', 'guest_gift', 'withdrawal', 'test', 'maintenance'
));
