-- A gift addressed to somebody: a Telegram @username or an e-mail address.
--
-- The Telegram username each customer last wrote to the bot with, so a gift
-- addressed to @username finds its customer. Telegram usernames move between
-- accounts, so one is held by at most one customer: recording it for one
-- customer takes it from any other. Anonymising a customer removes the row.
CREATE TABLE telegram_usernames (
  user_id uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
  username text NOT NULL,
  seen_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT telegram_usernames_shape CHECK (username ~ '^[a-z0-9_]{5,32}$')
);
CREATE UNIQUE INDEX telegram_usernames_username_idx ON telegram_usernames (username);

-- Who a gift is for and how it reached them. A paid gift addressed to a known
-- customer is claimed for them and they are told in the bot; one addressed to
-- an e-mail address no Telegram customer has is mailed a claim code of its
-- own, whose hash is kept here (the sender's code still works too). The
-- address is sealed with the installation's data key; its fingerprint is the
-- one contact_channels uses, so a known customer is found without decrypting.
-- The address is erased once the mail is queued; it is needed for nothing
-- after that.
CREATE TABLE gift_recipients (
  gift_id uuid PRIMARY KEY REFERENCES gifts (id) ON DELETE CASCADE,
  username text,
  email_ciphertext bytea,
  email_fingerprint bytea,
  claim_code_hash bytea,
  locale text NOT NULL DEFAULT 'ru',
  status text NOT NULL DEFAULT 'waiting',
  recipient_user_id uuid REFERENCES users (id) ON DELETE SET NULL,
  delivered_via text,
  delivered_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT gift_recipients_one_address CHECK (username IS NULL OR email_ciphertext IS NULL),
  CONSTRAINT gift_recipients_waiting_address CHECK (status <> 'waiting' OR username IS NOT NULL OR email_ciphertext IS NOT NULL),
  CONSTRAINT gift_recipients_username_shape CHECK (username IS NULL OR username ~ '^[a-z0-9_]{5,32}$'),
  CONSTRAINT gift_recipients_email_fingerprint CHECK ((email_ciphertext IS NULL) = (email_fingerprint IS NULL)),
  CONSTRAINT gift_recipients_claim_code_shape CHECK (claim_code_hash IS NULL OR octet_length(claim_code_hash) = 32),
  CONSTRAINT gift_recipients_locale_known CHECK (locale IN ('ru', 'en')),
  CONSTRAINT gift_recipients_status_known CHECK (status IN ('waiting', 'delivered', 'closed')),
  CONSTRAINT gift_recipients_delivered_via_known CHECK (delivered_via IS NULL OR delivered_via IN ('telegram', 'email')),
  CONSTRAINT gift_recipients_delivery_complete CHECK ((status = 'delivered') = (delivered_at IS NOT NULL AND delivered_via IS NOT NULL))
);
CREATE INDEX gift_recipients_waiting_idx ON gift_recipients (created_at) WHERE status = 'waiting';
CREATE INDEX gift_recipients_username_idx ON gift_recipients (username) WHERE status = 'waiting' AND username IS NOT NULL;
CREATE UNIQUE INDEX gift_recipients_claim_code_idx ON gift_recipients (claim_code_hash) WHERE claim_code_hash IS NOT NULL;
