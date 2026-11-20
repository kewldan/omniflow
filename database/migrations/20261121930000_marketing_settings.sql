-- A settings section for the marketing behaviour Bedolaga configures in its
-- admin: the daily limit of distinct promo codes, the start screen, tips, the
-- referral-code prompt, mandatory acceptance of the terms, and what happens to
-- the subscriptions of customers who blocked the bot.
ALTER TABLE installation_settings DROP CONSTRAINT installation_settings_section_known;
ALTER TABLE installation_settings ADD CONSTRAINT installation_settings_section_known CHECK (
  section = ANY (ARRAY[
    'branding', 'remnawave', 'telegram', 'operator_group', 'required_channels',
    'maintenance', 'notifications', 'telemetry', 'backup', 'security', 'ai', 'mcp',
    'customer_auth', 'theme', 'analytics',
    'bot', 'support', 'reports', 'access', 'moderation', 'marketing'
  ]::text[])
);
INSERT INTO installation_settings (section) VALUES ('marketing') ON CONFLICT (section) DO NOTHING;
