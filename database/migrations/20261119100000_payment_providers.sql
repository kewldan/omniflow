-- Every payment adapter Omniflow ships, including the providers carried over
-- from Bedolaga. The list stays explicit rather than a pattern: a provider
-- column that accepts any slug would let a typo in a builder create intents
-- nothing can settle. Keep it in step with payments.Slugs() and the OpenAPI
-- PaymentProvider enum.

ALTER TABLE auto_renew_settings DROP CONSTRAINT auto_renew_settings_provider_check;
ALTER TABLE auto_renew_settings ADD CONSTRAINT auto_renew_settings_provider_check CHECK (
  provider IN ('manual', 'telegram_stars', 'cryptobot', 'yookassa', 'heleket', 'mulenpay', 'pal24', 'platega', 'cashera', 'tribute', 'freekassa', 'kassa_ai', 'riopay', 'severpay', 'rollypay', 'aurapay', 'wata', 'cloudpayments', 'lava', 'overpay', 'paritypay', 'payhot', 'cispay', 'antilopay', 'anore', 'donut', 'etoplatezhi', 'jupiter', 'tabpay')
);

ALTER TABLE bot_checkout_sessions DROP CONSTRAINT bot_checkout_sessions_provider_check;
ALTER TABLE bot_checkout_sessions ADD CONSTRAINT bot_checkout_sessions_provider_check CHECK (
  provider IN ('manual', 'telegram_stars', 'cryptobot', 'yookassa', 'heleket', 'mulenpay', 'pal24', 'platega', 'cashera', 'tribute', 'freekassa', 'kassa_ai', 'riopay', 'severpay', 'rollypay', 'aurapay', 'wata', 'cloudpayments', 'lava', 'overpay', 'paritypay', 'payhot', 'cispay', 'antilopay', 'anore', 'donut', 'etoplatezhi', 'jupiter', 'tabpay')
);

ALTER TABLE payment_intents DROP CONSTRAINT payment_intents_provider_check;
ALTER TABLE payment_intents ADD CONSTRAINT payment_intents_provider_check CHECK (
  provider IN ('manual', 'telegram_stars', 'cryptobot', 'yookassa', 'heleket', 'mulenpay', 'pal24', 'platega', 'cashera', 'tribute', 'freekassa', 'kassa_ai', 'riopay', 'severpay', 'rollypay', 'aurapay', 'wata', 'cloudpayments', 'lava', 'overpay', 'paritypay', 'payhot', 'cispay', 'antilopay', 'anore', 'donut', 'etoplatezhi', 'jupiter', 'tabpay')
);

ALTER TABLE payment_methods DROP CONSTRAINT payment_methods_provider_check;
ALTER TABLE payment_methods ADD CONSTRAINT payment_methods_provider_check CHECK (
  provider IN ('manual', 'telegram_stars', 'cryptobot', 'yookassa', 'heleket', 'mulenpay', 'pal24', 'platega', 'cashera', 'tribute', 'freekassa', 'kassa_ai', 'riopay', 'severpay', 'rollypay', 'aurapay', 'wata', 'cloudpayments', 'lava', 'overpay', 'paritypay', 'payhot', 'cispay', 'antilopay', 'anore', 'donut', 'etoplatezhi', 'jupiter', 'tabpay')
);

ALTER TABLE payment_provider_settings DROP CONSTRAINT payment_provider_settings_provider_check;
ALTER TABLE payment_provider_settings ADD CONSTRAINT payment_provider_settings_provider_check CHECK (
  provider IN ('manual', 'telegram_stars', 'cryptobot', 'yookassa', 'heleket', 'mulenpay', 'pal24', 'platega', 'cashera', 'tribute', 'freekassa', 'kassa_ai', 'riopay', 'severpay', 'rollypay', 'aurapay', 'wata', 'cloudpayments', 'lava', 'overpay', 'paritypay', 'payhot', 'cispay', 'antilopay', 'anore', 'donut', 'etoplatezhi', 'jupiter', 'tabpay')
);
