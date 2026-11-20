-- Ukrainian, Chinese, and Persian, as Bedolaga offers them: a customer's
-- language and the bot preference may now name them. Content written per
-- language (plans, pages, templates) stays Russian and English; the new
-- languages read those in English.
ALTER TABLE users DROP CONSTRAINT users_locale_check;
ALTER TABLE users ADD CONSTRAINT users_locale_check CHECK (locale IN ('ru', 'en', 'uk', 'zh', 'fa'));
ALTER TABLE bot_preferences DROP CONSTRAINT bot_preferences_locale_check;
ALTER TABLE bot_preferences ADD CONSTRAINT bot_preferences_locale_check CHECK (locale IN ('auto', 'ru', 'en', 'uk', 'zh', 'fa'));
