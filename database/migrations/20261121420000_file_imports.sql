-- A customer import can come from an uploaded CSV as well as from the live
-- Remnawave API, so an operator moving from another panel or a Bedolaga export
-- can stage, review, and apply the same way.
ALTER TABLE customer_imports DROP CONSTRAINT customer_imports_source_check;
ALTER TABLE customer_imports ADD CONSTRAINT customer_imports_source_check
  CHECK (source = ANY (ARRAY['remnawave', 'file']::text[]));
