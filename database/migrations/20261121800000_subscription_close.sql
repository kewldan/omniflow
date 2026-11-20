-- A customer may close one of several subscriptions (Bedolaga's multi-tariff
-- deletion), and the installation decides what happens to its Remnawave user
-- (REMNAWAVE_USER_DELETE_MODE): disabled, which keeps it for an operator to
-- look at, or deleted.
ALTER TABLE subscription_policy
  ADD COLUMN remnawave_close_mode text NOT NULL DEFAULT 'disable',
  ADD CONSTRAINT subscription_policy_remnawave_close_mode_known
    CHECK (remnawave_close_mode IN ('disable', 'delete'));

ALTER TABLE fulfillment_operations DROP CONSTRAINT fulfillment_operations_operation_known;
ALTER TABLE fulfillment_operations ADD CONSTRAINT fulfillment_operations_operation_known CHECK (
  operation IN ('create', 'extend', 'enable', 'disable', 'reset_traffic', 'set_limits',
                'set_squads', 'reconcile', 'pause', 'resume', 'close')
);
