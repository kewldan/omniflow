-- Bind every OIDC identity to the issuer that asserted it.
--
-- A subject is only unique within one issuer. Until now a linked identity was
-- keyed by (provider row, subject), and a provider row's issuer could be edited
-- in place, so repointing a provider at an issuer someone else controls let
-- that issuer mint the subject of any linked operator or customer and sign in
-- as them. Storing the issuer on the binding, and requiring the ID token's
-- issuer to equal it at sign-in, makes a binding mean "this subject at this
-- issuer" — which is what it always should have meant.
--
-- Existing bindings were created against the issuer their provider holds right
-- now, so that is the value they are backfilled with.

ALTER TABLE admin_oidc_identities ADD COLUMN issuer text;

UPDATE admin_oidc_identities AS identity
SET issuer = provider.issuer
FROM admin_oidc_providers AS provider
WHERE provider.id = identity.provider_id;

ALTER TABLE admin_oidc_identities
  ALTER COLUMN issuer SET NOT NULL,
  ADD CONSTRAINT admin_oidc_identities_issuer_https CHECK (issuer ~ '^https://');

-- Customer identities share one table across Telegram and every OIDC provider.
-- The column is null for anything that is not OIDC. An OIDC identity whose
-- provider row has since been deleted has no issuer to backfill and stays null,
-- which the sign-in path treats as "matches no issuer": such a binding could
-- only ever be revived by re-creating the slug, possibly against a different
-- issuer, and that is exactly the case this migration exists to refuse.
ALTER TABLE identities ADD COLUMN oidc_issuer text;

UPDATE identities AS identity
SET oidc_issuer = provider.issuer
FROM customer_oidc_providers AS provider
WHERE identity.provider = 'oidc:' || provider.slug;

ALTER TABLE identities
  ADD CONSTRAINT identities_oidc_issuer_only_for_oidc CHECK (
    oidc_issuer IS NULL OR (provider LIKE 'oidc:%' AND oidc_issuer ~ '^https://')
  );
