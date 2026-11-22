package operator

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func payloadOf(t *testing.T, raw string) map[string]any {
	t.Helper()
	payload := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

var moscow = time.FixedZone("MSK", 3*3600)

const testCustomer = "6f1d0b6e-4b64-4c1e-9e3f-2b1f0f1a8d21"
const testOrder = "f38ec162-5e57-4252-b84d-2fbe2b708978"

// Every notice kind reads as a sentence with its numbers as money, in both
// languages, at the installation's time — and none of them is the raw
// "key: value" dump they replaced.
func TestEveryNoticeReadsAsASentence(t *testing.T) {
	customer := &customerFacts{ID: testCustomer, TelegramID: 777000, Username: "ivan", FirstName: "Ivan", LastName: "Petrov",
		Payments: 3, WalletMinor: 75000, WalletCurrency: "RUB"}
	order := &orderFacts{
		ID: testOrder, Operation: "extension", Currency: "RUB", PaidMinor: 149000, WalletMinor: 20000,
		DiscountMinor: 10000, PeriodSeconds: 90 * 86400, PlanName: "Premium", Provider: "yookassa", Method: "sbp",
		PromoCode: "AUTUMN",
	}
	cases := []struct {
		kind, payload string
		facts         noticeFacts
		en, ru        []string
	}{
		{"topup", `{"orderId":"` + testOrder + `","currency":"RUB","creditedMinor":50000,"classification":"paid"}`,
			noticeFacts{Customer: customer, Order: &orderFacts{ID: testOrder, Currency: "RUB", Provider: "yookassa", Method: "sbp"}},
			[]string{"💰 WALLET TOP-UP", "<b>Amount:</b> <b>+500 ₽</b>", "<b>Method:</b> YooKassa · SBP", "<b>Balance:</b> 250 ₽ → <b>750 ₽</b>",
				"<b>Username:</b> @ivan", "<b>Telegram ID:</b> <code>777000</code>", "<b>Payment:</b> 3rd", "<b>Customer:</b> Ivan Petrov"},
			[]string{"💰 ПОПОЛНЕНИЕ БАЛАНСА", "<b>Сумма:</b> <b>+500 ₽</b>", "<b>Способ:</b> YooKassa · СБП", "<b>Баланс:</b> 250 ₽ → <b>750 ₽</b>",
				"<b>Оплата:</b> 3-я", "<b>Пользователь:</b> Ivan Petrov"}},
		{"renewal", `{"orderId":"` + testOrder + `","operation":"extension","currency":"RUB","amountMinor":149000}`,
			noticeFacts{Customer: customer, Order: order},
			[]string{"♻️ RENEWAL", "<b>Plan:</b> Premium · 3 months", "<b>Paid:</b> <b>1 490 ₽</b>", "<b>Method:</b> YooKassa",
				"<b>From the wallet:</b> 200 ₽", "<b>Discount:</b> −100 ₽", "AUTUMN"},
			[]string{"♻️ ПРОДЛЕНИЕ", "<b>Тариф:</b> Premium · 3 месяца", "<b>Оплачено:</b> <b>1 490 ₽</b>", "<b>С баланса:</b> 200 ₽",
				"<b>Скидка:</b> −100 ₽", "промокод"}},
		{"purchase", `{"orderId":"` + testOrder + `","operation":"purchase","currency":"RUB","amountMinor":19900}`,
			noticeFacts{Customer: &customerFacts{ID: testCustomer, TelegramID: 5, Payments: 1}},
			[]string{"💳 NEW PURCHASE", "199 ₽", "🆕 <b>Payment:</b> first"},
			[]string{"💳 НОВАЯ ПОКУПКА", "<b>Оплата:</b> первая"}},
		{"topup", `{"event":"bedolaga_payment_needs_review","provider":"yookassa","providerReference":"yk-1","amountMinor":30000,"currency":"RUB","reason":"customer_not_found"}`,
			noticeFacts{},
			[]string{"OLD BOT'S PAYMENT NEEDS A DECISION", "<b>+300 ₽</b>", "<b>Method:</b> YooKassa", "no imported customer", "yk-1"},
			[]string{"ПЛАТЁЖ ИЗ СТАРОГО БОТА ЖДЁТ РЕШЕНИЯ", "нет такого перенесённого клиента", "<b>ID платежа:</b>"}},
		{"refund", `{"orderId":"` + testOrder + `","currency":"RUB","amountMinor":149000,"status":"refunded","operation":"purchase"}`,
			noticeFacts{Customer: customer},
			[]string{"↩️ REFUND", "−1 490 ₽", "refunded in full"},
			[]string{"↩️ ВОЗВРАТ", "возвращено полностью"}},
		{"incident", `{"orderId":"` + testOrder + `","provider":"yookassa","operation":"purchase","classification":"late","currency":"RUB","amountMinor":19900,"status":"payment_needs_attention","reason":"order_expired"}`,
			noticeFacts{},
			[]string{"🚨 A PAYMENT NEEDS ATTENTION", "<b>Amount:</b> <b>199 ₽</b>", "<b>Provider:</b> YooKassa", "paid after the order closed", "the order had expired"},
			[]string{"🚨 ПЛАТЁЖ ТРЕБУЕТ ВНИМАНИЯ", "оплачено после закрытия заказа", "заказ уже истёк"}},
		{"incident", `{"status":"maintenance_activated","source":"manual","reason":"panel upgrade"}`,
			noticeFacts{},
			[]string{"🛠 MAINTENANCE MODE IS ON", "switched by an operator", "panel upgrade"},
			[]string{"🛠 ВКЛЮЧЁН РЕЖИМ ОБСЛУЖИВАНИЯ", "включено оператором"}},
		{"fulfillment_failure", `{"entitlementId":"x","operation":"create","errorCode":"remnawave_unavailable","status":"retrying"}`,
			noticeFacts{},
			[]string{"⚠️ ACCESS NOT DELIVERED YET", "<b>Step:</b> creating access", "remnawave_unavailable", "retrying automatically"},
			[]string{"⚠️ ДОСТУП ПОКА НЕ ВЫДАН", "выдача доступа", "повторяется автоматически"}},
		{"backup", `{"status":"failed","fileName":"omniflow-2026.dump","source":"scheduled"}`,
			noticeFacts{},
			[]string{"💥 BACKUP FAILED", "<b>File:</b> <code>omniflow-2026.dump</code>"},
			[]string{"💥 РЕЗЕРВНАЯ КОПИЯ НЕ УДАЛАСЬ", "<b>Файл:</b>"}},
		{"security", `{"event":"admin.totp.disabled","adminUserId":"0d9b1f2c-6a44-4f0e-9d7a-11c2f3e4a5b6","method":"web"}`,
			noticeFacts{},
			[]string{"⚠️ TWO-FACTOR TURNED OFF", "<b>How:</b> in the panel", "0d9b1f2c"},
			[]string{"⚠️ ОТКЛЮЧЕНА ДВУХФАКТОРНАЯ ЗАЩИТА", "в панели"}},
		{"support", `{"ticketId":"0d9b1f2c-6a44-4f0e-9d7a-11c2f3e4a5b6","queueId":"q","event":"sla_breach","priority":"urgent","overdueMinutes":45}`,
			noticeFacts{Ticket: &ticketFacts{QueueName: "Payments", CustomerID: testCustomer}, Customer: customer},
			[]string{"⏰ FIRST REPLY IS OVERDUE", "<b>Queue:</b> Payments", "urgent", "45 min", "@ivan"},
			[]string{"⏰ ПРОСРОЧЕН ПЕРВЫЙ ОТВЕТ", "срочный", "45 мин"}},
		{"support", `{"ticketId":"0d9b1f2c-6a44-4f0e-9d7a-11c2f3e4a5b6","queueId":"q"}`,
			noticeFacts{Ticket: &ticketFacts{QueueName: "General", Subject: "Bonus", Message: "Where is it?"}},
			[]string{"🎫 NEW TICKET", "<b>ID:</b> <code>0d9b1f2c</code>", "<b>Subject:</b> Bonus", "<b>Message:</b>\nWhere is it?", "<b>Created:</b>"},
			[]string{"🎫 НОВЫЙ ТИКЕТ", "<b>Заголовок:</b> Bonus", "<b>Сообщение:</b>\nWhere is it?", "<b>Создан:</b>"}},
		{"anomaly", `{"signalId":"s","metric":"failed_payments","severity":"high","observed":12,"threshold":5,"subjectType":"provider"}`,
			noticeFacts{},
			[]string{"🔴 ANOMALY: FAILED PAYMENTS", "<b>Observed:</b> <b>12</b>", "<b>Threshold:</b> 5"},
			[]string{"🔴 АНОМАЛИЯ", "<b>Сейчас:</b> <b>12</b>", "<b>Порог:</b> 5"}},
		{"trial", `{"orderId":"` + testOrder + `","event":"trial_claimed"}`,
			noticeFacts{Customer: customer, Order: &orderFacts{PlanName: "Trial", PeriodSeconds: 3 * 86400}},
			[]string{"🎁 TRIAL STARTED", "<b>Plan:</b> Trial · 3 days"},
			[]string{"🎁 ВЗЯТ ПРОБНЫЙ ПЕРИОД", "3 дня"}},
		{"promo", `{"orderId":"` + testOrder + `","amountMinor":5000,"event":"promo_redeemed"}`,
			noticeFacts{Order: &orderFacts{Currency: "RUB", PromoCode: "AUTUMN"}},
			[]string{"🏷 PROMO CODE USED", "AUTUMN", "−50 ₽"},
			[]string{"🏷 ПРИМЕНЁН ПРОМОКОД", "Скидка"}},
		{"partner", `{"orderId":"` + testOrder + `","amountMinor":2500,"currency":"RUB","event":"referral_reward_referrer"}`,
			noticeFacts{Customer: customer},
			[]string{"🤝 REWARD TO THE INVITER", "+25 ₽"},
			[]string{"🤝 НАГРАДА ПРИГЛАСИВШЕМУ"}},
		{"addon", `{"orderId":"` + testOrder + `","entitlementId":"e","event":"addon_purchased"}`,
			noticeFacts{Order: &orderFacts{Currency: "RUB", PaidMinor: 9900, PlanName: "Premium"}},
			[]string{"➕ ADD-ON BOUGHT", "99 ₽"},
			[]string{"➕ КУПЛЕНО ДОПОЛНЕНИЕ"}},
		{"marketing", `{"event":"loyalty_tier","customerId":"` + testCustomer + `","fromTier":"silver","toTier":"gold","reason":"evaluation"}`,
			noticeFacts{},
			[]string{"🏅 LOYALTY TIER CHANGED", "silver", "gold", "recalculated"},
			[]string{"🏅 ИЗМЕНИЛСЯ УРОВЕНЬ ЛОЯЛЬНОСТИ", "пересчёт"}},
		{"node", `{"event":"node.connection_lost","source":"remnawave","node":"de-1","country":"DE"}`,
			noticeFacts{},
			[]string{"🔴 NODE DE-1", "Connection lost", "🇩🇪"},
			[]string{"🔴 НОДА DE-1"}},
		{"monitoring", `{"event":"devices_over_limit","users":"2","devices":"u1 (5/3), u2 (4/3)"}`,
			noticeFacts{},
			[]string{"📱 DEVICES OVER THE LIMIT", "<b>2</b>", "u1 (5/3)"},
			[]string{"📱 УСТРОЙСТВ БОЛЬШЕ ЛИМИТА", "Клиентов"}},
		{"error", `{"process":"bot","fingerprint":"abc123def456","occurrences":100}`,
			noticeFacts{},
			[]string{"🐞 ERROR IN BOT", "<b>Seen:</b> <b>100</b>", "abc123def456"},
			[]string{"🐞 ОШИБКА В BOT", "<b>Повторов:</b> <b>100</b>"}},
		{"infrastructure", `{"event":"update_available","version":"1.5.0","previousVersion":"1.4.1"}`,
			noticeFacts{},
			[]string{"🆕 A NEW VERSION IS OUT", "1.4.1", "1.5.0"},
			[]string{"🆕 ВЫШЛА НОВАЯ ВЕРСИЯ"}},
		{"infrastructure", `{"event":"bot_started","process":"bot","version":"1.4.1"}`,
			noticeFacts{},
			[]string{"🚀 BOT STARTED", "<b>Version:</b> <b>1.4.1</b>"},
			[]string{"🚀 BOT ЗАПУЩЕН", "Версия"}},
		{"receipt", `{"orderId":"` + testOrder + `","amountMinor":19900,"currency":"RUB","status":"issued","receiptUrl":"https://lknpd.nalog.ru/r/1"}`,
			noticeFacts{},
			[]string{"🧾 RECEIPT ISSUED", "199 ₽"},
			[]string{"🧾 ЧЕК ВЫДАН"}},
	}
	at := time.Date(2026, 11, 21, 21, 52, 46, 0, time.UTC)
	for _, test := range cases {
		for _, locale := range []string{"en", "ru"} {
			text, markup := renderNotice(renderInput{
				Kind: test.kind, Payload: payloadOf(t, test.payload), Facts: test.facts, At: at,
				Locale: locale, Location: moscow, PanelURL: "https://lk.example.test",
			})
			wants := test.en
			if locale == "ru" {
				wants = test.ru
			}
			for _, want := range wants {
				if !strings.Contains(text, want) {
					t.Errorf("%s/%s lacks %q:\n%s", test.kind, locale, want, text)
				}
			}
			for _, raw := range []string{"orderId:", "creditedMinor", "classification:", "2026-11-21T21:52:46Z"} {
				if strings.Contains(text, raw) {
					t.Errorf("%s/%s still dumps %q:\n%s", test.kind, locale, raw, text)
				}
			}
			when := "22 Nov, 00:52 MSK"
			if locale == "ru" {
				when = "22 ноя, 00:52 MSK"
			}
			if !strings.Contains(text, when) {
				t.Errorf("%s/%s is not in the installation's time %q:\n%s", test.kind, locale, when, text)
			}
			if strings.Contains(test.payload, testOrder) && test.kind != "security" {
				if markup == nil || !strings.Contains(markup.InlineKeyboard[0][0].URL+markup.InlineKeyboard[len(markup.InlineKeyboard)-1][len(markup.InlineKeyboard[len(markup.InlineKeyboard)-1])-1].URL, "/admin/") {
					t.Errorf("%s/%s has no panel button", test.kind, locale)
				}
			}
		}
	}
}

// A notice no template knows still reads as words, not identifiers.
func TestAnUnknownNoticeFallsBackToWords(t *testing.T) {
	text, _ := renderNotice(renderInput{
		Kind: "marketing", Payload: payloadOf(t, `{"event":"something_new","customerId":"`+testCustomer+`","status":"queued"}`),
		At: time.Unix(0, 0), Locale: "ru",
	})
	for _, want := range []string{"🎯 ЛОЯЛЬНОСТЬ И РЕФЕРАЛЫ", "Customer id", "Status"} {
		if !strings.Contains(text, want) {
			t.Fatalf("fallback lacks %q:\n%s", want, text)
		}
	}
}

// Nothing outside the allowlist reaches the chat, however the payload was
// filled: an e-mail, a link, or a message body stays out.
func TestANoticeCarriesNoContent(t *testing.T) {
	text, _ := renderNotice(renderInput{
		Kind: "support", Payload: payloadOf(t, `{"ticketId":"t","subject":"my card was charged twice","email":"a@b.c"}`),
		At: time.Unix(0, 0), Locale: "en",
	})
	for _, leaked := range []string{"charged twice", "a@b.c"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("%q leaked:\n%s", leaked, text)
		}
	}
	fallback, _ := renderNotice(renderInput{
		Kind: "anything", Payload: payloadOf(t, `{"subject":"secret text","token":"oft_x"}`), At: time.Unix(0, 0),
	})
	if strings.Contains(fallback, "secret text") || strings.Contains(fallback, "oft_x") {
		t.Fatalf("the fallback printed a field outside the allowlist:\n%s", fallback)
	}
}

func TestMoneyReadsAsMoney(t *testing.T) {
	for minor, want := range map[int64]string{50000: "500 ₽", 149050: "1 490.50 ₽", -2500: "−25 ₽"} {
		if got := formatMoney(minor, "RUB"); got != want {
			t.Fatalf("formatMoney(%d) = %q, want %q", minor, got, want)
		}
	}
	if got := formatMoney(250, "XTR"); got != "⭐ 250" {
		t.Fatalf("stars: %q", got)
	}
	if got := formatMoney(499, "USD"); got != "4.99 $" {
		t.Fatalf("dollars: %q", got)
	}
}
