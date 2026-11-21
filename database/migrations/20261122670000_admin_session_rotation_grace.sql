-- The token an operator session rotated away from, kept for a minute.
--
-- The panel fires several requests at once. When the session is due for
-- rotation one of them installs a new token and the rest arrive holding the
-- old one; refusing those answered 401 and cleared the browser's cookie — the
-- new one — so an operator was signed out at every rotation. The superseded
-- digest keeps resolving for a minute after `rotated_at`, without rotating
-- again and without touching the cookie.
ALTER TABLE admin_sessions
  ADD COLUMN previous_token_hash bytea,
  ADD CONSTRAINT admin_sessions_previous_token_hash_check
    CHECK (previous_token_hash IS NULL OR octet_length(previous_token_hash) = 32);

CREATE INDEX admin_sessions_previous_token_hash ON admin_sessions (previous_token_hash)
  WHERE previous_token_hash IS NOT NULL;
