-- A failed passkey sign-in has no actor yet: the credential is unknown or its
-- assertion was rejected before anyone was identified. The audit row is
-- written as "anonymous", which the constraint did not allow, so every such
-- failure was lost.
ALTER TABLE audit_events DROP CONSTRAINT audit_events_actor_type_known;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_actor_type_known
  CHECK (actor_type = ANY (ARRAY['customer'::text, 'operator'::text, 'admin'::text, 'system'::text, 'provider'::text, 'anonymous'::text]));
