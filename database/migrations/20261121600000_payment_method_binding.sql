-- A customer may ask, on a checkout, to keep the card or mandate they pay with
-- for automatic renewal. The consent is recorded on the payment intent at the
-- moment it is opened, so the settlement that later binds the provider's token
-- can show which explicit request it is honouring. It is never inferred: a
-- payment without this stamp never produces a saved method.
ALTER TABLE payment_intents ADD COLUMN save_method_consent_at timestamptz;

-- The intent a saved method was bound from, so a method traces back to the
-- payment the customer agreed on.
ALTER TABLE payment_methods ADD COLUMN source_payment_intent_id uuid REFERENCES payment_intents(id);

-- The terms of automatic payments, linked from the consent checkbox and the
-- auto-renew screen. It is an ordinary operator-editable info page; this only
-- gives a new installation a truthful default instead of a dead link. An
-- installation that already has a page with this slug keeps its own.
INSERT INTO info_pages (slug, kind, published_at, listed, sort_order)
VALUES ('recurring-terms', 'terms', now(), false, 90)
ON CONFLICT (slug) DO NOTHING;

INSERT INTO info_page_localizations (page_slug, locale, title, body)
VALUES
('recurring-terms', 'en', 'Terms of automatic payments',
'Automatic renewal is off until you turn it on. Turning it on, or ticking "Save this card" when you pay, is your consent to the charges described here.

## What is charged and when

- The price of the plan your subscription is on, in the currency you paid in.
- The charge is attempted shortly before the paid period ends — three days by default; you can choose the lead time on the Auto-renew screen or in the bot.
- You choose whether it is paid from your wallet balance or a saved card.

## If a charge fails

A declined charge is retried a few times over the following days. You are told when a retry fails and when renewal stops, after which you renew by hand as before.

## What is stored

Only a reference issued by the payment provider and a masked label such as the card scheme and its last four digits. The card number, expiry date and security code are never sent to or kept by this service.

## How to stop

Turn automatic renewal off or remove the saved card at any time on the Auto-renew screen of your account or in the bot. Nothing more is charged after that. Removing a card does not cancel a subscription that is already paid for.'),
('recurring-terms', 'ru', 'Условия автоплатежей',
'Автопродление выключено, пока вы его не включите. Включение автопродления или отметка «Сохранить карту» при оплате — ваше согласие на списания, описанные ниже.

## Что и когда списывается

- Стоимость тарифа, на котором находится подписка, в той валюте, в которой вы платили.
- Списание выполняется незадолго до конца оплаченного периода — по умолчанию за три дня; срок можно выбрать на экране «Автопродление» или в боте.
- Вы выбираете, списывать ли с баланса кошелька или с сохранённой карты.

## Если списание не прошло

Отклонённое списание повторяется несколько раз в течение следующих дней. Мы сообщим, если повтор не прошёл и когда автопродление остановится; после этого продлевать можно вручную, как раньше.

## Что хранится

Только ссылка, выданная платёжным провайдером, и маска вида «платёжная система и последние четыре цифры». Номер карты, срок действия и код безопасности сервису не передаются и не хранятся.

## Как отключить

Выключите автопродление или удалите сохранённую карту в любой момент на экране «Автопродление» в личном кабинете или в боте. После этого ничего не списывается. Удаление карты не отменяет уже оплаченную подписку.')
ON CONFLICT DO NOTHING;
