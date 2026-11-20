-- Limited grace access (Bedolaga's grace access). When a subscription expires
-- or runs out of traffic, the installation may keep the Remnawave user alive
-- on one operator-chosen squad (a "Telegram only" squad, say) with a small
-- traffic allowance for a few days, instead of letting it stop. Paying for
-- anything on the subscription restores the full plan.
ALTER TABLE subscription_policy
  ADD COLUMN limited_access_on_expiry boolean NOT NULL DEFAULT false,
  ADD COLUMN limited_access_on_traffic boolean NOT NULL DEFAULT false,
  ADD COLUMN limited_access_squad_id uuid,
  ADD COLUMN limited_access_traffic_gb integer,
  ADD COLUMN limited_access_days integer,
  ADD CONSTRAINT subscription_policy_limited_access_complete CHECK (
    (NOT limited_access_on_expiry AND NOT limited_access_on_traffic)
    OR (limited_access_squad_id IS NOT NULL
        AND limited_access_days BETWEEN 1 AND 90
        AND limited_access_traffic_gb BETWEEN 1 AND 1000)
  );

-- One limited access period of one entitlement. Each trigger opens at most one
-- per entitlement, so a period that runs out without payment cannot open
-- itself again; a subscription holds at most one active period at a time.
CREATE TABLE limited_access_sessions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL,
  subscription_id uuid NOT NULL REFERENCES subscriptions (id),
  entitlement_id uuid NOT NULL REFERENCES entitlements (id),
  trigger text NOT NULL,
  squad_id uuid NOT NULL,
  traffic_bytes bigint NOT NULL,
  -- The bytes the user had used when the period began, read from Remnawave
  -- by the operation that applied it. The limit pushed is this plus
  -- traffic_bytes, so the allowance is fresh without resetting the counter
  -- the full plan resumes from.
  base_used_bytes bigint,
  starts_at timestamptz NOT NULL DEFAULT now(),
  ends_at timestamptz NOT NULL,
  status text NOT NULL DEFAULT 'active',
  ended_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT limited_access_sessions_trigger_known CHECK (trigger IN ('expiry', 'traffic')),
  CONSTRAINT limited_access_sessions_status_known CHECK (status IN ('active', 'ended', 'restored')),
  CONSTRAINT limited_access_sessions_period CHECK (ends_at > starts_at),
  CONSTRAINT limited_access_sessions_amounts CHECK (traffic_bytes > 0 AND (base_used_bytes IS NULL OR base_used_bytes >= 0)),
  CONSTRAINT limited_access_sessions_ended_consistent CHECK ((status = 'active') = (ended_at IS NULL)),
  CONSTRAINT limited_access_sessions_once UNIQUE (entitlement_id, trigger)
);
CREATE UNIQUE INDEX limited_access_sessions_one_active
  ON limited_access_sessions (subscription_id) WHERE status = 'active';

ALTER TABLE fulfillment_operations DROP CONSTRAINT fulfillment_operations_operation_known;
ALTER TABLE fulfillment_operations ADD CONSTRAINT fulfillment_operations_operation_known CHECK (
  operation IN ('create', 'extend', 'enable', 'disable', 'reset_traffic', 'set_limits', 'set_squads',
                'reconcile', 'pause', 'resume', 'close', 'limit_access', 'restore_access')
);
