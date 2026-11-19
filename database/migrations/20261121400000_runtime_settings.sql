-- Installation settings used to be a record of intent: saved, versioned,
-- audited, and read by nothing. The processes now read them, and the sections
-- that configure the bot, support, operator reports, sign-up access, and
-- moderation are added alongside the original ten.
ALTER TABLE installation_settings DROP CONSTRAINT installation_settings_section_known;
ALTER TABLE installation_settings ADD CONSTRAINT installation_settings_section_known CHECK (
  section = ANY (ARRAY[
    'branding', 'remnawave', 'telegram', 'operator_group', 'required_channels',
    'maintenance', 'notifications', 'telemetry', 'backup', 'security', 'ai', 'mcp',
    'customer_auth', 'theme', 'analytics',
    'bot', 'support', 'reports', 'access', 'moderation'
  ]::text[])
);

INSERT INTO installation_settings (section) VALUES
  ('bot'), ('support'), ('reports'), ('access'), ('moderation')
ON CONFLICT (section) DO NOTHING;

-- A pass whose cadence an operator can change runs on a short River tick and
-- claims each real run here, so the interval is read at run time and several
-- worker replicas still run a due pass exactly once.
CREATE TABLE periodic_task_runs (
  name text NOT NULL,
  last_started_at timestamptz NOT NULL,
  CONSTRAINT periodic_task_runs_pkey PRIMARY KEY (name),
  CONSTRAINT periodic_task_runs_name_present CHECK (length(name) BETWEEN 1 AND 64)
);
