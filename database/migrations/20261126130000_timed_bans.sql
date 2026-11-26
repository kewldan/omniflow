-- A ban can end on its own. A suspended customer with suspension_ends_at set
-- is restored by the worker once the moment passes; a null value is a ban
-- with no end. Restoring by hand clears it.
ALTER TABLE users ADD COLUMN suspension_ends_at timestamptz;

CREATE INDEX users_suspension_ends
  ON users (suspension_ends_at)
  WHERE status = 'suspended' AND suspension_ends_at IS NOT NULL;
