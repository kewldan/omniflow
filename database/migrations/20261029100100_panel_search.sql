-- Record-level search in the operator panel's command palette.
--
-- Almost everything the palette looks up is already indexed: identifiers are
-- primary keys (a prefix becomes a range scan on the key), a Telegram id and
-- an email fingerprint each have a unique index, and a Remnawave username has
-- one. Two lookups did not.
--
-- A ticket subject is free text, and "the ticket about the refund" is how a
-- support agent remembers one. A trigram index answers an infix match without
-- reading every ticket. pg_trgm ships with PostgreSQL and is a trusted
-- extension, so a database owner without superuser can create it.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX support_tickets_subject_trgm_idx
  ON support_tickets USING gin (subject gin_trgm_ops);

-- A provider's payment reference is what a customer quotes from their bank
-- or wallet, without knowing which provider issued it. The existing unique
-- index leads with the provider, so a lookup by reference alone needs its own.
CREATE INDEX payment_intents_provider_reference_lookup_idx
  ON payment_intents (provider_reference) WHERE provider_reference IS NOT NULL;
