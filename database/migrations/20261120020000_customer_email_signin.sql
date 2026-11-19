-- E-mail sign-in. A link is issued either to a customer found by a verified
-- e-mail contact (user_id set) or, for an address no customer holds yet, to
-- the address itself (email_* set, user_id NULL): the customer is created only
-- when the link is redeemed, so typing somebody else's address creates nothing.
ALTER TABLE customer_magic_links ALTER COLUMN user_id DROP NOT NULL;
ALTER TABLE customer_magic_links ADD COLUMN email_fingerprint bytea;
ALTER TABLE customer_magic_links ADD COLUMN email_ciphertext bytea;
ALTER TABLE customer_magic_links ADD CONSTRAINT customer_magic_links_target_known CHECK (
  (user_id IS NOT NULL) OR (email_fingerprint IS NOT NULL AND email_ciphertext IS NOT NULL)
);
CREATE INDEX customer_magic_links_email_recent
  ON customer_magic_links (email_fingerprint, created_at)
  WHERE email_fingerprint IS NOT NULL;
