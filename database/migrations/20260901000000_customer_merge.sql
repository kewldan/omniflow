-- Merging two customer accounts.
--
-- Linking an identity to the account you are already signed in to works. Joining
-- two accounts that both exist does not, and that is the case that actually
-- occurs: a customer buys in Telegram, later signs in on the web through a
-- provider the first account never carried, and ends up holding an empty second
-- account with their subscription on the other one.
--
-- Forty-six tables reference `users`, so a merge is not "update a foreign key".
-- It is a decision about each category of thing a customer owns, and several of
-- those decisions are refusals. What this migration adds is the record that the
-- merge happened and the state that makes it impossible to do twice.

ALTER TABLE users
  -- The account this one was merged into. Set exactly when the status is
  -- `merged`, which is what makes the operation idempotent: a second attempt
  -- finds a source that is already merged and reports the first merge rather
  -- than moving anything again.
  ADD COLUMN merged_into uuid REFERENCES users(id),
  ADD COLUMN merged_at timestamptz;

ALTER TABLE users
  DROP CONSTRAINT users_status_check;

ALTER TABLE users
  ADD CONSTRAINT users_status_known CHECK (
    status IN ('active', 'suspended', 'deleted', 'merged')
  );

-- The three columns are one fact. A merged account with no target would be a
-- customer nobody can find their subscription under.
ALTER TABLE users
  ADD CONSTRAINT users_merge_complete CHECK (
    (status = 'merged') = (merged_into IS NOT NULL)
    AND (merged_into IS NULL) = (merged_at IS NULL)
  );

-- An account cannot be merged into itself, which would make the whole record
-- meaningless while looking like a successful operation.
ALTER TABLE users
  ADD CONSTRAINT users_merge_not_self CHECK (merged_into IS NULL OR merged_into <> id);

CREATE INDEX users_merged_into_idx ON users (merged_into) WHERE merged_into IS NOT NULL;

-- The lifecycle log gains the two events, so a merge appears in the same place
-- a suspension and a deletion do rather than only in the operator audit trail.
-- The customer's own history is what support reads first.
ALTER TABLE customer_lifecycle_events
  DROP CONSTRAINT customer_lifecycle_events_action_check;

ALTER TABLE customer_lifecycle_events
  ADD CONSTRAINT customer_lifecycle_events_action_known CHECK (
    action IN ('suspended', 'restored', 'deletion_requested', 'deleted',
               'anonymized', 'merged_away', 'merged_in')
  );

COMMENT ON COLUMN users.merged_into IS
  'Where this account went. A merged account keeps its rows and its history; everything transferable was moved to the target.';
