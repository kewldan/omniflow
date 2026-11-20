-- A scan of which customers have blocked the bot. The panel asks for one; the
-- bot, which holds the Telegram client, runs it and records progress here.
CREATE TABLE blocker_scans (
  id uuid DEFAULT gen_random_uuid() NOT NULL,
  status text NOT NULL DEFAULT 'pending',
  checked integer NOT NULL DEFAULT 0,
  blocked integer NOT NULL DEFAULT 0,
  requested_by uuid,
  requested_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  finished_at timestamptz,
  CONSTRAINT blocker_scans_pkey PRIMARY KEY (id),
  CONSTRAINT blocker_scans_requested_by_fkey FOREIGN KEY (requested_by) REFERENCES admin_users(id) ON DELETE SET NULL,
  CONSTRAINT blocker_scans_status_known CHECK (status = ANY (ARRAY['pending', 'running', 'completed', 'failed']::text[])),
  CONSTRAINT blocker_scans_counts_valid CHECK (checked >= 0 AND blocked >= 0 AND blocked <= checked)
);
-- At most one scan waits or runs at a time.
CREATE UNIQUE INDEX blocker_scans_one_active ON blocker_scans ((true)) WHERE status IN ('pending', 'running');
