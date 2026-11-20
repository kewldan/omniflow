-- Backups from the web panel. The api has no pg_dump, so the panel records
-- what an operator asked for — take a backup now, restore one, delete one —
-- and the worker, which holds the tools and the shared directory, does it.
CREATE TABLE backup_requests (
  id uuid DEFAULT gen_random_uuid() NOT NULL,
  action text NOT NULL,
  backup_id uuid,
  reason text NOT NULL DEFAULT '',
  requested_by uuid,
  status text NOT NULL DEFAULT 'pending',
  error_code text,
  created_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz,
  CONSTRAINT backup_requests_pkey PRIMARY KEY (id),
  CONSTRAINT backup_requests_backup_fkey FOREIGN KEY (backup_id) REFERENCES backups(id) ON DELETE SET NULL,
  CONSTRAINT backup_requests_requested_by_fkey FOREIGN KEY (requested_by) REFERENCES admin_users(id) ON DELETE SET NULL,
  CONSTRAINT backup_requests_action_known CHECK (action = ANY (ARRAY['create', 'restore', 'delete']::text[])),
  CONSTRAINT backup_requests_status_known CHECK (status = ANY (ARRAY['pending', 'running', 'completed', 'failed']::text[])),
  CONSTRAINT backup_requests_target CHECK ((action = 'create') = (backup_id IS NULL)),
  CONSTRAINT backup_requests_restore_reason CHECK (action <> 'restore' OR length(btrim(reason)) > 0)
);
CREATE INDEX backup_requests_pending_idx ON backup_requests (created_at) WHERE status = 'pending';

-- An encrypted backup file an operator uploaded to restore from.
ALTER TABLE backups DROP CONSTRAINT backups_kind_check;
ALTER TABLE backups ADD CONSTRAINT backups_kind_check
  CHECK (kind = ANY (ARRAY['scheduled', 'manual', 'uploaded']::text[]));
