-- Temporary access to one squad for a number of hours (Bedolaga's
-- SubscriptionTemporaryAccess). An operator grants it from the customer card,
-- or a promo code does; the worker takes it back when the hours are up. The
-- squad is added on top of whatever the entitlement holds, through the
-- fulfillment pipeline, and never written to the entitlement itself.
CREATE TABLE squad_access_grants (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL,
  subscription_id uuid NOT NULL REFERENCES subscriptions (id),
  squad_id uuid NOT NULL,
  source text NOT NULL,
  granted_by uuid,
  order_id uuid REFERENCES orders (id),
  reason text,
  idempotency_key text NOT NULL UNIQUE,
  starts_at timestamptz NOT NULL DEFAULT now(),
  ends_at timestamptz NOT NULL,
  status text NOT NULL DEFAULT 'active',
  ended_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT squad_access_grants_source_known CHECK (source IN ('operator', 'promo')),
  CONSTRAINT squad_access_grants_status_known CHECK (status IN ('active', 'ended', 'revoked')),
  CONSTRAINT squad_access_grants_hours CHECK (ends_at > starts_at AND ends_at <= starts_at + interval '720 hours'),
  CONSTRAINT squad_access_grants_ended_consistent CHECK ((status = 'active') = (ended_at IS NULL)),
  CONSTRAINT squad_access_grants_reason_length CHECK (reason IS NULL OR char_length(reason) <= 400)
);
CREATE INDEX squad_access_grants_active_idx ON squad_access_grants (subscription_id) WHERE status = 'active';

ALTER TABLE fulfillment_operations DROP CONSTRAINT fulfillment_operations_operation_known;
ALTER TABLE fulfillment_operations ADD CONSTRAINT fulfillment_operations_operation_known CHECK (
  operation IN ('create', 'extend', 'enable', 'disable', 'reset_traffic', 'set_limits', 'set_squads',
                'reconcile', 'pause', 'resume', 'close', 'limit_access', 'restore_access',
                'grant_squad', 'revoke_squad')
);

-- A promo code may grant it: the squad and, in value, the hours.
ALTER TABLE promotions ADD COLUMN squad_id uuid;
ALTER TABLE promotions DROP CONSTRAINT promotions_kind_known;
ALTER TABLE promotions ADD CONSTRAINT promotions_kind_known CHECK (
  kind IN ('percent', 'fixed', 'wallet_credit', 'days', 'trial', 'balance_days', 'traffic',
           'loyalty_tier', 'squad_access')
);
ALTER TABLE promotions ADD CONSTRAINT promotions_squad_access_complete CHECK (
  kind <> 'squad_access' OR (squad_id IS NOT NULL AND value BETWEEN 1 AND 720)
);
