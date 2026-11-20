-- Customers who stopped using the service are marked dormant rather than
-- deleted. Bedolaga deletes inactive users outright; here dormancy is its own
-- state beside the account status, because status 'deleted' starts the privacy
-- deletion and retention flow, which destroys data a returning customer would
-- want back. A dormant customer who signs in or writes to the bot is revived
-- on the spot. Deleting dormant customers is a separate, opt-in step that goes
-- through the ordinary deletion flow.
ALTER TABLE users ADD COLUMN dormant_at timestamptz;
CREATE INDEX users_dormant_idx ON users (dormant_at) WHERE dormant_at IS NOT NULL;
COMMENT ON COLUMN users.dormant_at IS 'When the inactivity pass marked the customer dormant. Cleared by any activity.';

ALTER TABLE customer_lifecycle_events DROP CONSTRAINT customer_lifecycle_events_action_known;
ALTER TABLE customer_lifecycle_events ADD CONSTRAINT customer_lifecycle_events_action_known CHECK (
  action = ANY (ARRAY[
    'suspended', 'restored', 'deletion_requested', 'deleted', 'anonymized', 'merged_away', 'merged_in',
    'dormant', 'revived'
  ]::text[])
);

-- The thresholds live in their own settings section so an owner can change
-- them in the panel.
ALTER TABLE installation_settings DROP CONSTRAINT installation_settings_section_known;
ALTER TABLE installation_settings ADD CONSTRAINT installation_settings_section_known CHECK (
  section = ANY (ARRAY[
    'branding', 'remnawave', 'telegram', 'operator_group', 'required_channels',
    'maintenance', 'notifications', 'telemetry', 'backup', 'security', 'ai', 'mcp',
    'customer_auth', 'theme', 'analytics',
    'bot', 'support', 'reports', 'access', 'moderation', 'marketing', 'lifecycle'
  ]::text[])
);
INSERT INTO installation_settings (section) VALUES ('lifecycle') ON CONFLICT (section) DO NOTHING;

-- Any activity revives a dormant customer. The bot and both web panels all
-- record activity by moving users.last_activity_at, so reviving here covers
-- every surface at once and none of them can forget to.
CREATE FUNCTION revive_dormant_customer() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF OLD.dormant_at IS NOT NULL
     AND NEW.last_activity_at IS DISTINCT FROM OLD.last_activity_at
     AND NEW.dormant_at IS NOT DISTINCT FROM OLD.dormant_at THEN
    NEW.dormant_at := NULL;
    INSERT INTO customer_lifecycle_events (user_id, action, reason, actor_type)
    VALUES (NEW.id, 'revived', 'came back after being dormant', 'customer');
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER users_revive_dormant BEFORE UPDATE OF last_activity_at ON users
  FOR EACH ROW EXECUTE FUNCTION revive_dormant_customer();
