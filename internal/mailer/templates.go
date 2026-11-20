package mailer

import (
	"fmt"
	"html"
	"strings"
)

// Content is what a template fills in; Render turns it into a message with a
// plain-text part and an HTML part built from the same paragraphs, so the two
// can never say different things.
type Content struct {
	Subject    string
	Greeting   string
	Paragraphs []string
	// ActionLabel and ActionURL render a button in HTML and a bare link in text.
	ActionLabel string
	ActionURL   string
	// Footer is small print under the body.
	Footer string
	// UnsubscribeURL, when set, adds the footer link and the RFC 8058
	// one-click headers. Every marketing message has one; a transactional
	// message never does, because it is not something a customer can opt out of.
	UnsubscribeURL string
	Locale         string
}

// Render builds the message for one recipient.
func Render(to string, content Content) Message {
	var text strings.Builder
	var body strings.Builder
	if content.Greeting != "" {
		text.WriteString(content.Greeting + "\n\n")
		body.WriteString(`<p style="margin:0 0 16px">` + html.EscapeString(content.Greeting) + `</p>`)
	}
	for _, paragraph := range content.Paragraphs {
		text.WriteString(paragraph + "\n\n")
		body.WriteString(`<p style="margin:0 0 16px;white-space:pre-line">` + html.EscapeString(paragraph) + `</p>`)
	}
	if content.ActionURL != "" {
		text.WriteString(content.ActionLabel + ":\n" + content.ActionURL + "\n\n")
		body.WriteString(`<p style="margin:24px 0"><a href="` + html.EscapeString(content.ActionURL) +
			`" style="background:#111827;color:#ffffff;padding:12px 20px;border-radius:8px;text-decoration:none;display:inline-block">` +
			html.EscapeString(content.ActionLabel) + `</a></p>`)
	}
	footer := content.Footer
	if content.UnsubscribeURL != "" {
		line := Text(content.Locale, "unsubscribe_line")
		footer = strings.TrimSpace(footer + "\n" + line)
	}
	if footer != "" {
		text.WriteString("--\n" + footer + "\n")
		body.WriteString(`<p style="margin:32px 0 0;color:#6b7280;font-size:12px;white-space:pre-line">` +
			html.EscapeString(footer))
		if content.UnsubscribeURL != "" {
			body.WriteString(` <a href="` + html.EscapeString(content.UnsubscribeURL) + `" style="color:#6b7280">` +
				html.EscapeString(Text(content.Locale, "unsubscribe_action")) + `</a>`)
		}
		body.WriteString(`</p>`)
	}
	if content.UnsubscribeURL != "" {
		text.WriteString(Text(content.Locale, "unsubscribe_action") + ": " + content.UnsubscribeURL + "\n")
	}
	direction := "ltr"
	if content.Locale == "fa" {
		direction = "rtl"
	}
	message := Message{
		To: to, Subject: content.Subject, Text: text.String(),
		HTML: `<!doctype html><html dir="` + direction + `"><body style="font-family:-apple-system,Segoe UI,Roboto,sans-serif;` +
			`font-size:15px;line-height:1.5;color:#111827;max-width:560px;margin:0 auto;padding:24px">` +
			body.String() + `</body></html>`,
	}
	if content.UnsubscribeURL != "" {
		message.Headers = map[string]string{
			"List-Unsubscribe":      "<" + content.UnsubscribeURL + ">",
			"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
		}
	} else {
		message.Headers = map[string]string{"Auto-Submitted": "auto-generated"}
	}
	return message
}

// Text returns one catalogue string in the locale, falling back to English.
func Text(locale, key string, arguments ...any) string {
	value, ok := catalogue[normalizeLocale(locale)][key]
	if !ok {
		value = catalogue["en"][key]
	}
	if len(arguments) > 0 {
		return fmt.Sprintf(value, arguments...)
	}
	return value
}

func normalizeLocale(locale string) string {
	locale = strings.ToLower(strings.TrimSpace(locale))
	if _, ok := catalogue[locale]; ok {
		return locale
	}
	return "en"
}

// SignIn is the e-mailed sign-in link.
func SignIn(to, locale, link string, minutes int) Message {
	return Render(to, Content{
		Locale: locale, Subject: Text(locale, "signin_subject"),
		Paragraphs:  []string{Text(locale, "signin_body", minutes)},
		ActionLabel: Text(locale, "signin_action"), ActionURL: link,
		Footer: Text(locale, "signin_footer"),
	})
}

// Receipt confirms a payment.
type Receipt struct {
	OrderNumber string
	Amount      string
	Paid        string
	Description string
	OrdersURL   string
}

// ReceiptMessage renders a payment receipt.
func ReceiptMessage(to, locale string, receipt Receipt) Message {
	paragraphs := []string{
		Text(locale, "receipt_body"),
		Text(locale, "receipt_lines", receipt.OrderNumber, receipt.Description, receipt.Amount, receipt.Paid),
	}
	return Render(to, Content{
		Locale: locale, Subject: Text(locale, "receipt_subject", receipt.OrderNumber),
		Paragraphs: paragraphs, ActionLabel: Text(locale, "receipt_action"), ActionURL: receipt.OrdersURL,
		Footer: Text(locale, "receipt_footer"),
	})
}

// Broadcast renders an operator-written marketing message.
func Broadcast(to, locale, subject, body, unsubscribeURL string) Message {
	return Render(to, Content{
		Locale: locale, Subject: subject, Paragraphs: []string{body}, UnsubscribeURL: unsubscribeURL,
	})
}

// Offer renders a personal offer.
func Offer(to, locale, title, terms, expires, link, unsubscribeURL string) Message {
	paragraphs := []string{title}
	if strings.TrimSpace(terms) != "" {
		paragraphs = append(paragraphs, terms)
	}
	paragraphs = append(paragraphs, Text(locale, "offer_expires", expires))
	return Render(to, Content{
		Locale: locale, Subject: Text(locale, "offer_subject", title), Paragraphs: paragraphs,
		ActionLabel: Text(locale, "offer_action"), ActionURL: link, UnsubscribeURL: unsubscribeURL,
	})
}

// Test is the operator's test message.
func Test(to, locale string) Message {
	return Render(to, Content{
		Locale: locale, Subject: Text(locale, "test_subject"), Paragraphs: []string{Text(locale, "test_body")},
	})
}

// GuestPurchase tells a guest buyer their purchase is paid and how to use it.
func GuestPurchase(to, locale, plan, code, activateURL string, gift bool, recipientNotified bool) Message {
	paragraphs := []string{Text(locale, "guest_paid", plan)}
	switch {
	case gift && recipientNotified:
		paragraphs = append(paragraphs, Text(locale, "guest_gift_sent"))
	case gift:
		paragraphs = append(paragraphs, Text(locale, "guest_gift_link"), activateURL)
	default:
		paragraphs = append(paragraphs, Text(locale, "guest_activate"))
	}
	paragraphs = append(paragraphs, Text(locale, "guest_code", code))
	content := Content{
		Locale: locale, Subject: Text(locale, "guest_subject"), Paragraphs: paragraphs,
		Footer: Text(locale, "guest_footer"),
	}
	if !gift {
		content.ActionLabel, content.ActionURL = Text(locale, "guest_action"), activateURL
	}
	return Render(to, content)
}

// GuestGift tells a recipient somebody bought them a subscription.
func GuestGift(to, locale, plan, message, activateURL string) Message {
	paragraphs := []string{Text(locale, "gift_body", plan)}
	if strings.TrimSpace(message) != "" {
		paragraphs = append(paragraphs, "«"+message+"»")
	}
	return Render(to, Content{
		Locale: locale, Subject: Text(locale, "gift_subject"), Paragraphs: paragraphs,
		ActionLabel: Text(locale, "guest_action"), ActionURL: activateURL, Footer: Text(locale, "guest_footer"),
	})
}

// WithdrawalUpdate tells a customer what became of a withdrawal request.
func WithdrawalUpdate(to, locale, status, amount, comment string) Message {
	paragraphs := []string{Text(locale, "withdrawal_"+status, amount)}
	if strings.TrimSpace(comment) != "" {
		paragraphs = append(paragraphs, comment)
	}
	return Render(to, Content{
		Locale: locale, Subject: Text(locale, "withdrawal_subject"), Paragraphs: paragraphs,
	})
}

// catalogue holds every string a template uses. English is the fallback for
// a key or a locale that has none.
var catalogue = map[string]map[string]string{
	"en": {
		"unsubscribe_line":            "You receive this because you agreed to news and offers by e-mail.",
		"unsubscribe_action":          "Unsubscribe",
		"signin_subject":              "Sign in to your account",
		"signin_body":                 "Open the link below to sign in. It works once and expires in %d minutes.",
		"signin_action":               "Sign in",
		"signin_footer":               "If you did not ask to sign in, ignore this e-mail.",
		"receipt_subject":             "Payment received — order %s",
		"receipt_body":                "Thank you, your payment has been received.",
		"receipt_lines":               "Order: %s\nPurchase: %s\nAmount: %s\nPaid: %s",
		"receipt_action":              "Open my orders",
		"receipt_footer":              "This is a payment confirmation, not a tax receipt.",
		"offer_subject":               "A personal offer: %s",
		"offer_expires":               "The offer is valid until %s.",
		"offer_action":                "Use the offer",
		"test_subject":                "Test message",
		"maintenance_started_subject": "Maintenance has started",
		"maintenance_ended_subject":   "Maintenance is over",
		"maintenance_soon":            "soon",
		"test_body":                   "E-mail delivery is configured correctly.",
		"guest_subject":               "Your purchase is paid",
		"guest_paid":                  "Payment for %s has been received.",
		"guest_activate":              "Open the link below to activate the subscription. You will be signed in with this e-mail address; no password is needed.",
		"guest_gift_sent":             "We have e-mailed the recipient a link to activate it.",
		"guest_gift_link":             "Send this gift link to the recipient. Whoever opens it first and signs in receives the subscription:",
		"guest_code":                  "Activation code: %s",
		"guest_action":                "Activate",
		"guest_footer":                "Keep this e-mail: the code is shown only here.",
		"gift_subject":                "You have received a gift",
		"gift_body":                   "Somebody has bought you %s.",
		"withdrawal_subject":          "Withdrawal request update",
		"withdrawal_approved":         "Your withdrawal request for %s has been approved and will be paid out to the details you provided.",
		"withdrawal_rejected":         "Your withdrawal request for %s has been declined, and the amount has been returned to your balance.",
		"withdrawal_paid":             "Your withdrawal of %s has been paid out.",
	},
	"ru": {
		"unsubscribe_line":            "Вы получили это письмо, потому что согласились на новости и предложения по e-mail.",
		"unsubscribe_action":          "Отписаться",
		"signin_subject":              "Вход в личный кабинет",
		"signin_body":                 "Чтобы войти, откройте ссылку ниже. Она работает один раз и действует %d минут.",
		"signin_action":               "Войти",
		"signin_footer":               "Если вы не запрашивали вход, просто проигнорируйте это письмо.",
		"receipt_subject":             "Оплата получена — заказ %s",
		"receipt_body":                "Спасибо, оплата получена.",
		"receipt_lines":               "Заказ: %s\nПокупка: %s\nСумма: %s\nОплачено: %s",
		"receipt_action":              "Открыть мои заказы",
		"receipt_footer":              "Это подтверждение оплаты, а не фискальный чек.",
		"offer_subject":               "Персональное предложение: %s",
		"offer_expires":               "Предложение действует до %s.",
		"offer_action":                "Воспользоваться",
		"test_subject":                "Тестовое письмо",
		"maintenance_started_subject": "Начались технические работы",
		"maintenance_ended_subject":   "Технические работы завершены",
		"maintenance_soon":            "скоро",
		"test_body":                   "Отправка e-mail настроена правильно.",
		"guest_subject":               "Покупка оплачена",
		"guest_paid":                  "Оплата за «%s» получена.",
		"guest_activate":              "Откройте ссылку ниже, чтобы активировать подписку. Вход выполнится по этому адресу e-mail, пароль не нужен.",
		"guest_gift_sent":             "Мы отправили получателю письмо со ссылкой для активации.",
		"guest_gift_link":             "Отправьте эту ссылку получателю подарка. Подписку получит тот, кто первым откроет её и войдёт:",
		"guest_code":                  "Код активации: %s",
		"guest_action":                "Активировать",
		"guest_footer":                "Сохраните это письмо: код показывается только здесь.",
		"gift_subject":                "Вам подарок",
		"gift_body":                   "Вам подарили «%s».",
		"withdrawal_subject":          "Заявка на вывод средств",
		"withdrawal_approved":         "Заявка на вывод %s одобрена, деньги будут переведены по указанным реквизитам.",
		"withdrawal_rejected":         "Заявка на вывод %s отклонена, сумма возвращена на баланс.",
		"withdrawal_paid":             "Вывод %s выполнен.",
	},
}
