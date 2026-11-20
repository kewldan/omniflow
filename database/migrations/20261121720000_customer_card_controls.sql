-- Operator controls on the customer card that need state of their own.

-- A restriction stops one kind of spending — topping up the wallet, or buying
-- anything — without suspending the account, and says why. One row per kind;
-- lifting it deletes the row, and both acts are in the audit trail.
CREATE TABLE customer_restrictions (
  user_id uuid NOT NULL,
  kind text NOT NULL,
  reason text NOT NULL,
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT customer_restrictions_pkey PRIMARY KEY (user_id, kind),
  CONSTRAINT customer_restrictions_user_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
  CONSTRAINT customer_restrictions_created_by_fkey FOREIGN KEY (created_by) REFERENCES admin_users(id) ON DELETE SET NULL,
  CONSTRAINT customer_restrictions_kind_known CHECK (kind = ANY (ARRAY['topup', 'purchase']::text[])),
  CONSTRAINT customer_restrictions_reason_present CHECK (length(btrim(reason)) BETWEEN 1 AND 500)
);

-- When the customer last did anything in the bot or the web panel, written at
-- most once every few minutes per customer.
ALTER TABLE users ADD COLUMN last_activity_at timestamptz;

-- What the customer did, as route names: which screen or button, on which
-- surface. It is the customer's action log on the card and, aggregated with
-- no customer in it, the bot's button statistics. Never free text. Kept for
-- ninety days.
CREATE TABLE customer_activity (
  id bigint GENERATED ALWAYS AS IDENTITY,
  user_id uuid NOT NULL,
  surface text NOT NULL,
  action text NOT NULL,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT customer_activity_pkey PRIMARY KEY (id),
  CONSTRAINT customer_activity_user_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
  CONSTRAINT customer_activity_surface_known CHECK (surface = ANY (ARRAY['bot', 'web']::text[])),
  CONSTRAINT customer_activity_action_shape CHECK (action ~ '^[a-z0-9_:./-]{1,64}$')
);
CREATE INDEX customer_activity_user_idx ON customer_activity (user_id, occurred_at DESC);
CREATE INDEX customer_activity_time_idx ON customer_activity (occurred_at);

-- Bulk actions gain adding traffic and setting the device limit.
ALTER TABLE bulk_operations DROP CONSTRAINT bulk_operations_kind_check;
ALTER TABLE bulk_operations ADD CONSTRAINT bulk_operations_kind_check CHECK (
  kind = ANY (ARRAY[
    'customer_export', 'subscription_extend', 'subscription_disable', 'subscription_enable',
    'wallet_credit', 'subscription_add_traffic', 'subscription_set_devices'
  ]::text[])
);
