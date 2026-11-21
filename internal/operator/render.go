package operator

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/payments"
)

// Readable notices.
//
// A notice is read in a chat by somebody deciding whether to act, so it says
// what happened in words, with the amount as money, the customer as the handle
// an operator recognises, the time in the installation's own timezone, and a
// button to the record. What it may say is unchanged: identifiers, amounts,
// plan names, and the customer's Telegram handle — never anything a customer
// wrote, an operator's address, a token, or a provider payload.

// noticeFacts is what the dispatcher looked up in the database for a notice:
// the order, the customer, and the ticket it names. Every part is optional; a
// notice whose records cannot be read still renders from its payload.
type noticeFacts struct {
	Order    *orderFacts
	Customer *customerFacts
	Ticket   *ticketFacts
}

type orderFacts struct {
	ID            string
	Operation     string
	Currency      string
	SubtotalMinor int64
	DiscountMinor int64
	WalletMinor   int64
	PaidMinor     int64
	PeriodSeconds int64
	PlanName      string
	Provider      string
	Method        string
	PromoCode     string
}

type customerFacts struct {
	ID         string
	TelegramID int64
	Username   string
	// Payments counts the provider-settled orders and a previous bot's
	// payments, the current one included.
	Payments int64
	// WalletMinor is the balance in WalletCurrency now.
	WalletMinor    int64
	WalletCurrency string
}

type ticketFacts struct {
	ID         string
	QueueName  string
	Priority   string
	Status     string
	CustomerID string
}

// renderInput is one notice to render.
type renderInput struct {
	Kind     string
	Payload  map[string]any
	Facts    noticeFacts
	At       time.Time
	Locale   string
	Location *time.Location
	PanelURL string
}

// phrase is one string in the two languages the operator group speaks.
type phrase struct{ en, ru string }

func (input renderInput) say(text phrase) string {
	if input.Locale == "ru" && text.ru != "" {
		return text.ru
	}
	return text.en
}

func (input renderInput) str(key string) string {
	switch value := input.Payload[key].(type) {
	case string:
		return value
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	default:
		return fmt.Sprint(value)
	}
}

func (input renderInput) num(key string) (int64, bool) {
	switch value := input.Payload[key].(type) {
	case float64:
		return int64(value), true
	case json.Number:
		parsed, err := value.Int64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

// notice is a rendered message under construction.
type notice struct {
	input   renderInput
	title   string
	lines   []string
	buttons []models.InlineKeyboardButton
}

func (n *notice) line(emoji, text string) {
	if strings.TrimSpace(text) != "" {
		n.lines = append(n.lines, emoji+" "+text)
	}
}

func (n *notice) button(text, path string) {
	base := strings.TrimSuffix(strings.TrimSpace(n.input.PanelURL), "/")
	if !strings.HasPrefix(base, "https://") || path == "" {
		return
	}
	for _, existing := range n.buttons {
		if existing.URL == base+path {
			return
		}
	}
	n.buttons = append(n.buttons, models.InlineKeyboardButton{Text: text, URL: base + path})
}

func (n *notice) link(text, target string) {
	if strings.HasPrefix(target, "https://") {
		n.buttons = append(n.buttons, models.InlineKeyboardButton{Text: text, URL: target})
	}
}

func (n *notice) finish() (string, *models.InlineKeyboardMarkup) {
	builder := &strings.Builder{}
	builder.WriteString("<b>" + n.title + "</b>\n\n")
	for _, line := range n.lines {
		builder.WriteString(line + "\n")
	}
	builder.WriteString("\n🕐 " + n.input.when())
	var markup *models.InlineKeyboardMarkup
	if len(n.buttons) > 0 {
		rows := [][]models.InlineKeyboardButton{}
		for start := 0; start < len(n.buttons); start += 2 {
			rows = append(rows, n.buttons[start:min(start+2, len(n.buttons))])
		}
		markup = &models.InlineKeyboardMarkup{InlineKeyboard: rows}
	}
	return builder.String(), markup
}

var monthsRU = []string{"янв", "фев", "мар", "апр", "мая", "июн", "июл", "авг", "сен", "окт", "ноя", "дек"}

// when is the notice's time in the installation's timezone.
func (input renderInput) when() string {
	location := input.Location
	if location == nil {
		location = time.UTC
	}
	local := input.At.In(location)
	zone, _ := local.Zone()
	if input.Locale == "ru" {
		return fmt.Sprintf("%d %s, %s %s", local.Day(), monthsRU[local.Month()-1], local.Format("15:04"), zone)
	}
	return local.Format("2 Jan, 15:04") + " " + zone
}

var currencySymbols = map[string]string{"RUB": "₽", "USD": "$", "EUR": "€", "UAH": "₴", "KZT": "₸", "BYN": "Br", "GBP": "£"}

// formatMoney renders minor units as an amount a person reads: "1 490 ₽",
// "4.99 $", "⭐ 250".
func formatMoney(minor int64, currency string) string {
	currency = strings.ToUpper(currency)
	sign := ""
	if minor < 0 {
		sign, minor = "−", -minor
	}
	if currency == "XTR" {
		return sign + "⭐ " + groupDigits(minor)
	}
	exponent, err := payments.CurrencyExponent(currency)
	if err != nil {
		exponent = 2
	}
	scale := int64(math.Pow10(exponent))
	amount := groupDigits(minor / scale)
	if fraction := minor % scale; exponent > 0 && fraction != 0 {
		amount += "." + fmt.Sprintf("%0*d", exponent, fraction)
	}
	symbol := currencySymbols[currency]
	if symbol == "" {
		symbol = currency
	}
	return sign + amount + " " + symbol
}

// period renders a duration in whole days, months, or years.
func (input renderInput) period(seconds int64) string {
	days := seconds / 86400
	if days <= 0 {
		return ""
	}
	if input.Locale == "ru" {
		switch {
		case days%365 == 0:
			return pluralRU(days/365, "год", "года", "лет")
		case days%30 == 0:
			return pluralRU(days/30, "месяц", "месяца", "месяцев")
		default:
			return pluralRU(days, "день", "дня", "дней")
		}
	}
	unit := func(count int64, one, many string) string {
		if count == 1 {
			return "1 " + one
		}
		return strconv.FormatInt(count, 10) + " " + many
	}
	switch {
	case days%365 == 0:
		return unit(days/365, "year", "years")
	case days%30 == 0:
		return unit(days/30, "month", "months")
	default:
		return unit(days, "day", "days")
	}
}

func pluralRU(count int64, one, few, many string) string {
	form := many
	switch remainder := count % 100; {
	case remainder >= 11 && remainder <= 14:
	case count%10 == 1:
		form = one
	case count%10 >= 2 && count%10 <= 4:
		form = few
	}
	return strconv.FormatInt(count, 10) + " " + form
}

// ordinal is "2nd payment" / "2-я оплата".
func (input renderInput) paymentCount(count int64) string {
	if count <= 1 {
		return input.say(phrase{"🆕 first payment", "🆕 первая оплата"})
	}
	if input.Locale == "ru" {
		return fmt.Sprintf("🔁 %d-я оплата", count)
	}
	suffix := "th"
	if count%100 < 11 || count%100 > 13 {
		switch count % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("🔁 %d%s payment", count, suffix)
}

var providerNames = map[string]string{
	"yookassa": "YooKassa", "cryptobot": "CryptoBot", "telegram_stars": "Telegram Stars", "stars": "Telegram Stars",
	"heleket": "Heleket", "platega": "Platega", "mulenpay": "MulenPay", "pal24": "PayPalych", "wata": "WATA",
	"freekassa": "FreeKassa", "tribute": "Tribute", "cloudpayments": "CloudPayments", "manual": "manual",
	"stripe": "Stripe", "robokassa": "Robokassa", "lava": "Lava", "kassa_ai": "Kassa AI", "overpay": "Overpay",
	"antilopay": "Antilopay", "riopay": "RioPay", "severpay": "SeverPay", "paypear": "PayPear", "rollypay": "RollyPay",
	"aurapay": "AuraPay", "fragment_direct": "Fragment", "fragment": "Fragment", "wallet": "wallet",
	"cashera": "Cashera", "paritypay": "ParityPay", "payhot": "PayHot", "cispay": "CISPay", "anore": "Anore",
	"donut": "Donut", "etoplatezhi": "Это Платежи", "jupiter": "Jupiter", "tabpay": "TabPay",
}

var paymentMethodNames = map[string]phrase{
	"sbp": {"SBP", "СБП"}, "card": {"card", "карта"}, "bank_card": {"card", "карта"},
	"sberpay": {"SberPay", "SberPay"}, "sber_pay": {"SberPay", "SberPay"}, "tpay": {"T-Pay", "T-Pay"},
	"tinkoff_pay": {"T-Pay", "T-Pay"}, "yoo_money": {"YooMoney", "ЮMoney"}, "yoomoney": {"YooMoney", "ЮMoney"},
	"crypto": {"crypto", "криптовалюта"}, "usdt": {"USDT", "USDT"}, "ton": {"TON", "TON"}, "qr": {"QR", "QR"},
}

// via is "YooKassa · SBP": the provider and, when there is one, the method.
func (input renderInput) via(provider, method string) string {
	text := providerName(provider)
	if method != "" {
		if name, ok := paymentMethodNames[strings.ToLower(method)]; ok {
			text += " · " + input.say(name)
		} else {
			text += " · " + method
		}
	}
	return html.EscapeString(text)
}

func providerName(slug string) string {
	if name := providerNames[strings.ToLower(slug)]; name != "" {
		return name
	}
	return slug
}

// humanize turns an identifier like "payment_needs_attention" into words.
func humanize(value string) string {
	value = strings.NewReplacer("_", " ", ".", " ", "-", " ").Replace(value)
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func code(value string) string { return "<code>" + html.EscapeString(value) + "</code>" }

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// customerLine names the customer: their Telegram handle and ID, or the
// customer ID when the account has no Telegram.
func (n *notice) customer(customerID string) {
	facts := n.input.Facts.Customer
	if facts != nil && facts.ID != "" {
		customerID = facts.ID
	}
	parts := []string{}
	if facts != nil {
		if facts.Username != "" {
			parts = append(parts, "@"+html.EscapeString(facts.Username))
		}
		if facts.TelegramID != 0 {
			parts = append(parts, code(strconv.FormatInt(facts.TelegramID, 10)))
		}
	}
	if len(parts) == 0 && customerID != "" {
		parts = append(parts, code(shortID(customerID)))
	}
	if len(parts) > 0 {
		n.line("👤", strings.Join(parts, " · "))
	}
	if customerID != "" {
		n.button(n.input.say(phrase{"👤 Customer", "👤 Клиент"}), "/admin/customers/"+url.PathEscape(customerID))
	}
}

func (n *notice) orderButton(orderID string) {
	if orderID != "" {
		n.button(n.input.say(phrase{"🧾 Order", "🧾 Заказ"}), "/admin/finance/"+url.PathEscape(orderID))
	}
}

// orderLines writes what an order bought and how it was paid.
func (n *notice) orderLines(order *orderFacts, amountMinor int64, currency string) {
	if order != nil {
		plan := html.EscapeString(order.PlanName)
		if plan != "" {
			if period := n.input.period(order.PeriodSeconds); period != "" {
				plan = "<b>" + plan + "</b> · " + period
			} else {
				plan = "<b>" + plan + "</b>"
			}
			n.line("📦", plan)
		}
		if currency == "" {
			currency = order.Currency
		}
		if amountMinor == 0 {
			amountMinor = order.PaidMinor
		}
	}
	if amountMinor > 0 && currency != "" {
		paid := n.input.say(phrase{"Paid", "Оплачено"}) + ": <b>" + formatMoney(amountMinor, currency) + "</b>"
		if order != nil && order.Provider != "" {
			paid += " " + n.input.say(phrase{"via", "через"}) + " " + n.input.via(order.Provider, order.Method)
		}
		n.line("💰", paid)
	}
	if order == nil {
		return
	}
	if order.WalletMinor > 0 {
		n.line("👛", n.input.say(phrase{"From the wallet", "С баланса"})+": "+formatMoney(order.WalletMinor, order.Currency))
	}
	if order.DiscountMinor > 0 {
		discount := n.input.say(phrase{"Discount", "Скидка"}) + ": −" + formatMoney(order.DiscountMinor, order.Currency)
		if order.PromoCode != "" {
			discount += " · " + n.input.say(phrase{"promo", "промокод"}) + " " + code(order.PromoCode)
		}
		n.line("🏷", discount)
	}
}

func (n *notice) paymentNumber() {
	if facts := n.input.Facts.Customer; facts != nil && facts.Payments > 0 {
		n.lines = append(n.lines, n.input.paymentCount(facts.Payments))
	}
}

func (n *notice) walletBalance() {
	if facts := n.input.Facts.Customer; facts != nil && facts.WalletCurrency != "" {
		n.line("💼", n.input.say(phrase{"Balance now", "Баланс теперь"})+": <b>"+formatMoney(facts.WalletMinor, facts.WalletCurrency)+"</b>")
	}
}

// renderNotice renders any operator notice except the shop sale and the
// scheduled report, which have renderers of their own.
func renderNotice(input renderInput) (string, *models.InlineKeyboardMarkup) {
	n := &notice{input: input}
	event := input.str("event")
	status := input.str("status")
	orderID := input.str("orderId")
	customerID := input.str("customerId")
	if customerID == "" && input.Facts.Ticket != nil {
		customerID = input.Facts.Ticket.CustomerID
	}
	amount, _ := input.num("amountMinor")
	currency := input.str("currency")

	switch input.Kind {
	case "purchase", "renewal", "addon":
		n.renderSale(event, orderID, customerID, amount, currency)
	case "topup":
		n.renderTopUp(event, orderID, customerID, currency)
	case "refund":
		n.renderRefund(status, orderID, customerID, amount, currency)
	case "incident":
		n.renderIncident(status, orderID, customerID, amount, currency)
	case "fulfillment_failure":
		n.renderFulfillment(status, orderID, customerID)
	case "backup":
		n.renderBackup(status)
	case "security":
		n.renderSecurity(event, customerID)
	case "support":
		n.renderSupport(event, customerID)
	case "anomaly":
		n.renderAnomaly()
	case "trial":
		n.title = input.say(phrase{"🎁 Trial started", "🎁 Взят пробный период"})
		n.customer(customerID)
		n.orderLines(input.Facts.Order, 0, "")
		n.orderButton(orderID)
	case "promo":
		n.title = input.say(phrase{"🏷 Promo code used", "🏷 Применён промокод"})
		n.customer(customerID)
		if order := input.Facts.Order; order != nil && order.PromoCode != "" {
			n.line("🎟", code(order.PromoCode))
		}
		if amount > 0 {
			discountCurrency := currency
			if discountCurrency == "" && input.Facts.Order != nil {
				discountCurrency = input.Facts.Order.Currency
			}
			n.line("💸", input.say(phrase{"Discount", "Скидка"})+": <b>−"+formatMoney(amount, discountCurrency)+"</b>")
		}
		n.orderButton(orderID)
	case "partner":
		n.renderPartner(event, orderID, customerID, amount, currency)
	case "marketing":
		n.renderMarketing(event, customerID)
	case "node":
		n.renderNode(event)
	case "monitoring":
		n.renderMonitoring(event, customerID)
	case "error":
		n.renderError()
	case "infrastructure":
		n.renderInfrastructure(event)
	case "receipt":
		n.renderReceipt(status, orderID, amount, currency)
	default:
		n.renderFields()
	}
	if n.title == "" {
		n.renderFields()
	}
	return n.finish()
}

var saleTitles = map[string]phrase{
	"purchase":         {"💳 New purchase", "💳 Новая покупка"},
	"extension":        {"♻️ Renewal", "♻️ Продление"},
	"renewal":          {"♻️ Automatic renewal", "♻️ Автопродление"},
	"upgrade":          {"⬆️ Upgrade", "⬆️ Повышение тарифа"},
	"downgrade":        {"⬇️ Downgrade", "⬇️ Понижение тарифа"},
	"gift":             {"🎁 Gift bought", "🎁 Куплен подарок"},
	"addon":            {"➕ Add-on bought", "➕ Куплено дополнение"},
	"traffic_reset":    {"🔄 Traffic reset bought", "🔄 Куплен сброс трафика"},
	"traffic_change":   {"📶 Traffic changed", "📶 Изменён трафик"},
	"device_reduction": {"📱 Fewer devices", "📱 Уменьшено число устройств"},
	"squad_removal":    {"🌐 Server removed", "🌐 Убран сервер"},
	"code":             {"🎟 Code bought", "🎟 Куплен код"},
	"goods":            {"🛍 Shop order", "🛍 Заказ в магазине"},
}

func (n *notice) renderSale(event, orderID, customerID string, amount int64, currency string) {
	operation := n.input.str("operation")
	if operation == "" && n.input.Facts.Order != nil {
		operation = n.input.Facts.Order.Operation
	}
	if n.input.Kind == "addon" || event == "addon_purchased" {
		operation = "addon"
	}
	title, ok := saleTitles[operation]
	if !ok {
		title = saleTitles["purchase"]
	}
	n.title = n.input.say(title)
	n.customer(customerID)
	n.orderLines(n.input.Facts.Order, amount, currency)
	n.paymentNumber()
	n.orderButton(orderID)
}

func (n *notice) renderTopUp(event, orderID, customerID, currency string) {
	credited, _ := n.input.num("creditedMinor")
	if credited == 0 {
		credited, _ = n.input.num("amountMinor")
	}
	switch event {
	case "bedolaga_payment_credited":
		n.title = n.input.say(phrase{"👛 Old bot's payment credited", "👛 Зачислен платёж из старого бота"})
	case "bedolaga_payment_needs_review":
		n.title = n.input.say(phrase{"⚠️ Old bot's payment needs a decision", "⚠️ Платёж из старого бота ждёт решения"})
	default:
		switch n.input.str("classification") {
		case "late":
			n.title = n.input.say(phrase{"👛 Late top-up credited", "👛 Зачислено запоздавшее пополнение"})
		case "reconciled":
			n.title = n.input.say(phrase{"👛 Top-up found by reconciliation", "👛 Пополнение найдено сверкой"})
		default:
			n.title = n.input.say(phrase{"👛 Wallet top-up", "👛 Пополнение баланса"})
		}
	}
	n.customer(customerID)
	if credited != 0 && currency != "" {
		line := "<b>+" + formatMoney(credited, currency) + "</b>"
		if provider := n.input.str("provider"); provider != "" {
			line += " " + n.input.say(phrase{"via", "через"}) + " " + html.EscapeString(providerName(provider))
		} else if order := n.input.Facts.Order; order != nil && order.Provider != "" {
			line += " " + n.input.say(phrase{"via", "через"}) + " " + n.input.via(order.Provider, order.Method)
		}
		n.line("💰", line)
	}
	if event != "bedolaga_payment_needs_review" {
		n.walletBalance()
	}
	if reason := n.input.str("reason"); reason != "" {
		n.line("❓", reasonText(n.input, reason))
	}
	if reference := n.input.str("providerReference"); reference != "" {
		n.line("🔖", code(reference))
	}
	if event == "" {
		n.paymentNumber()
	}
	n.orderButton(orderID)
}

var reasons = map[string]phrase{
	"guest_purchase":            {"a landing or gift purchase, not a top-up", "покупка с лендинга или подарок, а не пополнение"},
	"customer_not_found":        {"no imported customer matches it", "нет такого перенесённого клиента"},
	"telegram_mismatch":         {"the Telegram ID does not match the customer", "Telegram ID не совпадает с клиентом"},
	"customer_inactive":         {"the customer is not active", "клиент не активен"},
	"interrupted_after_marker":  {"interrupted after the payment was sent", "прервано после отправки оплаты"},
	"ambiguous_delivery":        {"the provider's answer was ambiguous", "ответ провайдера неоднозначен"},
	"operator_resolved_first":   {"an operator had already resolved it", "оператор уже закрыл заказ"},
	"order_cancelled":           {"the order was cancelled", "заказ был отменён"},
	"order_expired":             {"the order had expired", "заказ уже истёк"},
	"order_paid":                {"the order was already paid", "заказ уже оплачен"},
	"order_fulfilled":           {"the order was already fulfilled", "заказ уже выполнен"},
	"order_refunded":            {"the order was refunded", "по заказу был возврат"},
	"order_partially_refunded":  {"the order was partly refunded", "по заказу был частичный возврат"},
	"manual":                    {"switched by an operator", "включено оператором"},
	"scheduled":                 {"on schedule", "по расписанию"},
	"evaluation":                {"recalculated", "пересчёт"},
	"remnawave_unavailable":     {"Remnawave did not answer", "Remnawave не ответил"},
	"remnawave_user_not_found":  {"the panel user is gone", "пользователь панели не найден"},
	"customer_deletion_request": {"the customer asked to delete their data", "клиент попросил удалить данные"},
}

func reasonText(input renderInput, reason string) string {
	if text, ok := reasons[reason]; ok {
		return input.say(text)
	}
	return html.EscapeString(humanize(reason))
}

var refundStates = map[string]phrase{
	"refunded":           {"refunded in full", "возвращено полностью"},
	"partially_refunded": {"refunded in part", "возвращено частично"},
}

func (n *notice) renderRefund(status, orderID, customerID string, amount int64, currency string) {
	n.title = n.input.say(phrase{"↩️ Refund", "↩️ Возврат"})
	// A shop refund names the goods provider; an order refund names the
	// operation it reverses.
	if n.input.str("provider") != "" && n.input.str("operation") == "" {
		n.title = n.input.say(phrase{"↩️ Shop order refunded", "↩️ Возврат за заказ в магазине"})
	}
	n.customer(customerID)
	if amount > 0 && currency != "" {
		line := "<b>−" + formatMoney(amount, currency) + "</b>"
		if state, ok := refundStates[status]; ok {
			line += " · " + n.input.say(state)
		}
		n.line("💸", line)
	}
	if order := n.input.Facts.Order; order != nil && order.PlanName != "" {
		n.line("📦", html.EscapeString(order.PlanName))
	}
	if operation := n.input.str("operation"); operation != "" {
		if title, ok := saleTitles[operation]; ok {
			n.line("🧾", n.input.say(title))
		}
	}
	if wallet := n.input.str("wallet"); wallet != "" {
		n.line("👛", n.input.say(phrase{"Wallet", "Баланс"})+": "+html.EscapeString(humanize(wallet)))
	}
	if reason := n.input.str("reason"); reason != "" {
		n.line("❓", reasonText(n.input, reason))
	}
	if errorCode := n.input.str("errorCode"); errorCode != "" {
		n.line("🧩", code(errorCode))
	}
	n.orderButton(orderID)
}

var incidentTitles = map[string]phrase{
	"payment_needs_attention":   {"🚨 A payment needs attention", "🚨 Платёж требует внимания"},
	"payment_chargeback":        {"🚨 Chargeback", "🚨 Чарджбэк"},
	"refund_outcome_unknown":    {"❓ Refund outcome unknown", "❓ Итог возврата неизвестен"},
	"topup_refund_wallet_short": {"🚨 Refunded top-up: the wallet is short", "🚨 Возврат пополнения: на балансе не хватает"},
	"maintenance_activated":     {"🛠 Maintenance mode is on", "🛠 Включён режим обслуживания"},
	"maintenance_cleared":       {"✅ Maintenance mode is off", "✅ Режим обслуживания выключен"},
	"provider_balance_low":      {"🪫 The shop provider's balance is low", "🪫 У провайдера магазина мало средств"},
}

var classifications = map[string]phrase{
	"late":       {"paid after the order closed", "оплачено после закрытия заказа"},
	"duplicate":  {"paid twice", "оплачено дважды"},
	"mismatch":   {"the amount does not match", "сумма не совпадает"},
	"reconciled": {"found by reconciliation", "найдено сверкой"},
	"replay":     {"a replayed notification", "повторное уведомление"},
}

func (n *notice) renderIncident(status, orderID, customerID string, amount int64, currency string) {
	title, ok := incidentTitles[status]
	switch {
	case ok:
		n.title = n.input.say(title)
	case strings.HasPrefix(status, "delivered_after_"):
		n.title = n.input.say(phrase{"📦 Delivered after it was resolved", "📦 Выдано уже после закрытия"})
	default:
		n.title = "🚨 " + html.EscapeString(humanize(status))
	}
	if strings.HasPrefix(status, "maintenance_") {
		if source := n.input.str("source"); source != "" {
			n.line("⚙️", reasonText(n.input, source))
		}
		if reason := n.input.str("reason"); reason != "" {
			n.line("📝", html.EscapeString(reason))
		}
		return
	}
	if customerID != "" || n.input.Facts.Customer != nil {
		n.customer(customerID)
	}
	if amount > 0 && currency != "" {
		line := "<b>" + formatMoney(amount, currency) + "</b>"
		if provider := n.input.str("provider"); provider != "" {
			line += " · " + html.EscapeString(providerName(provider))
		}
		n.line("💰", line)
	} else if provider := n.input.str("provider"); provider != "" {
		n.line("🏦", html.EscapeString(providerName(provider)))
	}
	if classification := n.input.str("classification"); classification != "" {
		if text, ok := classifications[classification]; ok {
			n.line("🔎", n.input.say(text))
		} else {
			n.line("🔎", html.EscapeString(humanize(classification)))
		}
	}
	if reason := n.input.str("reason"); reason != "" {
		n.line("❓", reasonText(n.input, reason))
	}
	if errorCode := n.input.str("errorCode"); errorCode != "" {
		n.line("🧩", code(errorCode))
	}
	n.orderButton(orderID)
}

var operationNames = map[string]phrase{
	"create":         {"creating access", "выдача доступа"},
	"extend":         {"extending access", "продление доступа"},
	"update":         {"updating access", "изменение доступа"},
	"suspend":        {"suspending access", "приостановка доступа"},
	"resume":         {"resuming access", "возобновление доступа"},
	"revoke":         {"revoking access", "отзыв доступа"},
	"delete":         {"deleting access", "удаление доступа"},
	"reset_traffic":  {"resetting traffic", "сброс трафика"},
	"rotate":         {"rotating the link", "смена ссылки"},
	"sync":           {"syncing with the panel", "сверка с панелью"},
	"remove_devices": {"removing devices", "удаление устройств"},
}

func (n *notice) renderFulfillment(status, orderID, customerID string) {
	switch {
	case status == "needs_review":
		n.title = n.input.say(phrase{"⚠️ A shop order needs review", "⚠️ Заказ в магазине требует проверки"})
	case n.input.Payload["paidMinor"] != nil:
		n.title = n.input.say(phrase{"⚠️ Add-on paid with nothing to attach it to", "⚠️ Дополнение оплачено, но его некуда добавить"})
	default:
		n.title = n.input.say(phrase{"⚠️ Access not delivered yet", "⚠️ Доступ пока не выдан"})
	}
	if customerID != "" || n.input.Facts.Customer != nil {
		n.customer(customerID)
	}
	if operation := n.input.str("operation"); operation != "" {
		if text, ok := operationNames[operation]; ok {
			n.line("🔧", n.input.say(text))
		} else {
			n.line("🔧", html.EscapeString(humanize(operation)))
		}
	}
	if paid, ok := n.input.num("paidMinor"); ok && paid > 0 {
		n.line("💰", formatMoney(paid, n.input.str("currency")))
	}
	if provider := n.input.str("provider"); provider != "" {
		n.line("🏦", html.EscapeString(providerName(provider)))
	}
	if reason := n.input.str("reason"); reason != "" {
		n.line("❓", reasonText(n.input, reason))
	}
	if errorCode := n.input.str("errorCode"); errorCode != "" {
		n.line("🧩", code(errorCode))
	}
	if status == "retrying" {
		n.line("🔁", n.input.say(phrase{"Retrying automatically", "Повторяется автоматически"}))
	}
	n.orderButton(orderID)
}

func (n *notice) renderBackup(status string) {
	switch status {
	case "completed", "succeeded", "uploaded":
		n.title = n.input.say(phrase{"💾 Backup done", "💾 Резервная копия готова"})
	case "failed":
		n.title = n.input.say(phrase{"💥 Backup failed", "💥 Резервная копия не удалась"})
	case "started", "running":
		n.title = n.input.say(phrase{"💾 Backup started", "💾 Резервное копирование началось"})
	default:
		n.title = "💾 " + n.input.say(phrase{"Backup", "Резервная копия"}) + ": " + html.EscapeString(humanize(status))
	}
	if file := n.input.str("fileName"); file != "" {
		n.line("📄", code(file))
	}
	if size, ok := n.input.num("sizeBytes"); ok && size > 0 {
		n.line("📦", bytesText(size))
	}
	if source := n.input.str("source"); source != "" {
		n.line("⚙️", html.EscapeString(humanize(source)))
	}
}

func bytesText(size int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	value := float64(size)
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d B", size)
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}

var securityTitles = map[string]phrase{
	"admin.login.new_location":         {"🔐 Sign-in from a new place", "🔐 Вход из нового места"},
	"admin.password.changed":           {"🔑 Operator password changed", "🔑 Оператор сменил пароль"},
	"admin.password.reset_requested":   {"🔑 Password reset requested", "🔑 Запрошен сброс пароля"},
	"admin.password.reset_issued":      {"🔑 Password reset link issued", "🔑 Выдана ссылка сброса пароля"},
	"admin.password.reset_completed":   {"🔑 Password reset completed", "🔑 Пароль сброшен"},
	"admin.roles_changed":              {"🛡 Operator roles changed", "🛡 Изменены роли оператора"},
	"admin.owner_granted":              {"👑 Owner role granted", "👑 Выдана роль владельца"},
	"admin.totp.disabled":              {"⚠️ Two-factor turned off", "⚠️ Отключена двухфакторная защита"},
	"admin.totp.enabled":               {"🔐 Two-factor turned on", "🔐 Включена двухфакторная защита"},
	"admin.invitation.accepted":        {"🙋 Invitation accepted", "🙋 Приглашение принято"},
	"admin.oidc.auto_provisioned":      {"🙋 Operator created by single sign-on", "🙋 Оператор создан через единый вход"},
	"admin.passkey.registered":         {"🔑 Passkey added", "🔑 Добавлен ключ доступа"},
	"admin.passkey.revoked":            {"🔑 Passkey removed", "🔑 Удалён ключ доступа"},
	"admin.passkey.clone_detected":     {"🚨 Passkey clone suspected", "🚨 Подозрение на копию ключа доступа"},
	"customer_deletion_requested":      {"🗑 A customer asked to delete their data", "🗑 Клиент попросил удалить данные"},
	"admin.passkey.sign_count_rewound": {"🚨 Passkey counter went back", "🚨 Счётчик ключа доступа откатился"},
}

var methodNames = map[string]phrase{
	"web": {"in the panel", "в панели"}, "cli": {"from the command line", "из командной строки"},
	"password": {"with a password", "по паролю"}, "passkey": {"with a passkey", "ключом доступа"},
	"oidc": {"single sign-on", "единый вход"}, "self": {"by themselves", "сам"},
	"reset_token": {"with a reset link", "по ссылке сброса"}, "invitation": {"by invitation", "по приглашению"},
	"role_change": {"by a role change", "сменой роли"},
}

func (n *notice) renderSecurity(event, customerID string) {
	if title, ok := securityTitles[event]; ok {
		n.title = n.input.say(title)
	} else {
		n.title = "🔐 " + html.EscapeString(humanize(strings.TrimPrefix(event, "admin.")))
	}
	if method := n.input.str("method"); method != "" {
		if text, ok := methodNames[method]; ok {
			n.line("🧭", n.input.say(text))
		} else {
			n.line("🧭", html.EscapeString(humanize(method)))
		}
	}
	if admin := n.input.str("adminUserId"); admin != "" {
		n.line("👮", n.input.say(phrase{"Operator", "Оператор"})+" "+code(shortID(admin)))
		n.button(n.input.say(phrase{"👮 Operators", "👮 Операторы"}), "/admin/operators")
	}
	if event == "customer_deletion_requested" {
		n.customer(customerID)
		n.line("⏳", n.input.say(phrase{"Waiting for an operator", "Ждёт оператора"}))
	}
}

var priorityNames = map[string]phrase{
	"low": {"low", "низкий"}, "normal": {"normal", "обычный"}, "high": {"high", "высокий"}, "urgent": {"🔥 urgent", "🔥 срочный"},
}

func (n *notice) renderSupport(event, customerID string) {
	switch event {
	case "customer_reply":
		n.title = n.input.say(phrase{"💬 The customer replied", "💬 Клиент ответил"})
	case "sla_breach":
		n.title = n.input.say(phrase{"⏰ First reply is overdue", "⏰ Просрочен первый ответ"})
	default:
		n.title = n.input.say(phrase{"💬 New ticket", "💬 Новое обращение"})
	}
	n.customer(customerID)
	ticket := n.input.Facts.Ticket
	if ticket != nil && ticket.QueueName != "" {
		n.line("📂", html.EscapeString(ticket.QueueName))
	}
	priority := n.input.str("priority")
	if priority == "" && ticket != nil {
		priority = ticket.Priority
	}
	if text, ok := priorityNames[priority]; ok && priority != "normal" {
		n.line("⚡", n.input.say(phrase{"Priority", "Приоритет"})+": "+n.input.say(text))
	}
	if overdue, ok := n.input.num("overdueMinutes"); ok && overdue > 0 {
		n.line("⏱", n.input.say(phrase{"Overdue by", "Просрочено на"})+" <b>"+minutesText(n.input, overdue)+"</b>")
	}
	if id := n.input.str("ticketId"); id != "" {
		n.button(n.input.say(phrase{"💬 Open the ticket", "💬 Открыть обращение"}), "/admin/support?ticketId="+url.QueryEscape(id))
	}
}

func minutesText(input renderInput, minutes int64) string {
	if minutes >= 120 {
		hours := minutes / 60
		if input.Locale == "ru" {
			return pluralRU(hours, "час", "часа", "часов")
		}
		return fmt.Sprintf("%d h", hours)
	}
	if input.Locale == "ru" {
		return fmt.Sprintf("%d мин", minutes)
	}
	return fmt.Sprintf("%d min", minutes)
}

var severityMarks = map[string]string{"critical": "🔴", "high": "🔴", "medium": "🟠", "warning": "🟠", "low": "🟡", "info": "🔵"}

func (n *notice) renderAnomaly() {
	metric := n.input.str("metric")
	mark := severityMarks[strings.ToLower(n.input.str("severity"))]
	if mark == "" {
		mark = "📈"
	}
	n.title = mark + " " + n.input.say(phrase{"Anomaly", "Аномалия"}) + ": " + html.EscapeString(humanize(metric))
	observed, threshold := n.input.str("observed"), n.input.str("threshold")
	if observed != "" {
		line := n.input.say(phrase{"Observed", "Сейчас"}) + " <b>" + html.EscapeString(observed) + "</b>"
		if threshold != "" {
			line += " · " + n.input.say(phrase{"threshold", "порог"}) + " " + html.EscapeString(threshold)
		}
		n.line("📊", line)
	}
	if subject := n.input.str("subjectType"); subject != "" {
		n.line("🎯", html.EscapeString(humanize(subject)))
	}
	n.button(n.input.say(phrase{"🛡 Risk review", "🛡 Риски"}), "/admin/risk")
}

func (n *notice) renderPartner(event, orderID, customerID string, amount int64, currency string) {
	switch event {
	case "referral_reward_referrer":
		n.title = n.input.say(phrase{"🤝 Reward to the inviter", "🤝 Награда пригласившему"})
	case "referral_reward_referee":
		n.title = n.input.say(phrase{"🤝 Reward to the invited friend", "🤝 Награда приглашённому"})
	default:
		n.title = n.input.say(phrase{"🤝 Referral reward", "🤝 Реферальная награда"})
	}
	n.customer(customerID)
	if amount > 0 && currency != "" {
		n.line("💰", "<b>+"+formatMoney(amount, currency)+"</b>")
	}
	if order := n.input.Facts.Order; order != nil && order.PaidMinor > 0 {
		n.line("🧾", n.input.say(phrase{"For an order of", "За заказ на"})+" "+formatMoney(order.PaidMinor, order.Currency))
	}
	n.orderButton(orderID)
}

func (n *notice) renderMarketing(event, customerID string) {
	if event != "loyalty_tier" {
		n.renderFields()
		return
	}
	n.title = n.input.say(phrase{"🏅 Loyalty tier changed", "🏅 Изменился уровень лояльности"})
	n.customer(customerID)
	from, to := n.input.str("fromTier"), n.input.str("toTier")
	if from == "" {
		from = "—"
	}
	n.line("🔀", code(from)+" → "+code(to))
	if reason := n.input.str("reason"); reason != "" {
		n.line("❓", reasonText(n.input, reason))
	}
}

// countryFlag turns a two-letter country code into its flag.
func countryFlag(country string) string {
	country = strings.ToUpper(strings.TrimSpace(country))
	if len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' {
		return ""
	}
	return string(rune(0x1F1E6+int(country[0]-'A'))) + string(rune(0x1F1E6+int(country[1]-'A')))
}

func (n *notice) renderNode(event string) {
	lower := strings.ToLower(event)
	mark := "🖥"
	switch {
	case strings.Contains(lower, "lost") || strings.Contains(lower, "offline") || strings.Contains(lower, "disabled") || strings.Contains(lower, "deleted"):
		mark = "🔴"
	case strings.Contains(lower, "restored") || strings.Contains(lower, "online") || strings.Contains(lower, "enabled") || strings.Contains(lower, "created"):
		mark = "🟢"
	case strings.Contains(lower, "traffic"):
		mark = "📶"
	}
	node := n.input.str("node")
	n.title = mark + " " + n.input.say(phrase{"Node", "Нода"})
	if node != "" {
		n.title += " " + html.EscapeString(node)
	}
	n.line("📡", html.EscapeString(humanize(strings.TrimPrefix(event, "node."))))
	if country := n.input.str("country"); country != "" {
		n.line(firstNonEmpty(countryFlag(country), "🌍"), html.EscapeString(strings.ToUpper(country)))
	}
	if provider := n.input.str("provider"); provider != "" {
		n.line("🏢", html.EscapeString(provider))
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (n *notice) renderMonitoring(event, customerID string) {
	switch {
	case event == "devices_over_limit":
		n.title = n.input.say(phrase{"📱 Devices over the limit", "📱 Устройств больше лимита"})
		if users := n.input.str("users"); users != "" {
			n.line("👥", n.input.say(phrase{"Customers", "Клиентов"})+": <b>"+html.EscapeString(users)+"</b>")
		}
		if devices := n.input.str("devices"); devices != "" {
			n.line("📋", html.EscapeString(devices))
		}
	case strings.Contains(strings.ToLower(event), "torrent"):
		n.title = n.input.say(phrase{"🏴‍☠️ Torrent traffic blocked", "🏴‍☠️ Заблокирован торрент-трафик"})
		n.customer(customerID)
		if node := n.input.str("node"); node != "" {
			n.line("🖥", html.EscapeString(node))
		}
	default:
		n.title = "📈 " + html.EscapeString(humanize(event))
		n.customer(customerID)
	}
	if user := n.input.str("remnawaveUserId"); user != "" {
		n.line("🆔", "Remnawave "+code(user))
	}
}

func (n *notice) renderError() {
	process := n.input.str("process")
	n.title = "🐞 " + n.input.say(phrase{"Error in", "Ошибка в"}) + " " + html.EscapeString(firstNonEmpty(process, "?"))
	if occurrences, ok := n.input.num("occurrences"); ok {
		if occurrences <= 1 {
			n.line("🆕", n.input.say(phrase{"First time", "Впервые"}))
		} else {
			n.line("🔁", n.input.say(phrase{"Seen", "Повторилась"})+" <b>"+groupDigits(occurrences)+"</b> "+n.input.say(phrase{"times", "раз"}))
		}
	}
	if fingerprint := n.input.str("fingerprint"); fingerprint != "" {
		n.line("🧬", code(fingerprint))
	}
	n.button(n.input.say(phrase{"🛠 Error log", "🛠 Журнал ошибок"}), "/admin/system")
}

func (n *notice) renderInfrastructure(event string) {
	version := n.input.str("version")
	switch {
	case event == "update_available":
		n.title = n.input.say(phrase{"🆕 A new version is out", "🆕 Вышла новая версия"})
		n.line("📦", code(n.input.str("previousVersion"))+" → <b>"+html.EscapeString(version)+"</b>")
		if release := n.input.str("releaseUrl"); release != "" {
			n.link(n.input.say(phrase{"📝 What changed", "📝 Что нового"}), release)
		}
	case strings.HasSuffix(event, "_started"):
		process := strings.TrimSuffix(event, "_started")
		n.title = "🚀 " + html.EscapeString(firstNonEmpty(process, n.input.str("process"))) + " " + n.input.say(phrase{"started", "запущен"})
		if version != "" {
			n.line("🏷", n.input.say(phrase{"Version", "Версия"})+" <b>"+html.EscapeString(version)+"</b>")
		}
	default:
		n.title = "🛠 " + html.EscapeString(humanize(event))
		if version != "" {
			n.line("🏷", html.EscapeString(version))
		}
	}
}

func (n *notice) renderReceipt(status, orderID string, amount int64, currency string) {
	switch status {
	case "issued", "registered", "created":
		n.title = n.input.say(phrase{"🧾 Receipt issued", "🧾 Чек выдан"})
	case "failed", "error":
		n.title = n.input.say(phrase{"⚠️ Receipt not issued", "⚠️ Чек не выдан"})
	case "cancelled", "canceled":
		n.title = n.input.say(phrase{"🧾 Receipt cancelled", "🧾 Чек аннулирован"})
	default:
		n.title = "🧾 " + n.input.say(phrase{"Receipt", "Чек"}) + ": " + html.EscapeString(humanize(status))
	}
	if amount > 0 {
		n.line("💰", "<b>"+formatMoney(amount, firstNonEmpty(currency, "RUB"))+"</b>")
	}
	if receipt := n.input.str("receiptUrl"); receipt != "" {
		n.link(n.input.say(phrase{"🧾 Receipt", "🧾 Чек"}), receipt)
	}
	n.orderButton(orderID)
}

// renderFields is the fallback for a notice no template knows: its topic as
// the title and its allowlisted fields in words.
func (n *notice) renderFields() {
	if n.title == "" {
		n.title = html.EscapeString(topicTitle(n.input.Kind, n.input.Locale))
	}
	n.lines = nil
	keys := []string{}
	for _, field := range notificationFields {
		if value := n.input.str(field); value != "" {
			keys = append(keys, field)
		}
	}
	for _, field := range keys {
		n.line("▫️", html.EscapeString(fieldLabel(field))+": "+code(n.input.str(field)))
	}
	if id := n.input.str("orderId"); id != "" {
		n.orderButton(id)
	}
	if id := n.input.str("customerId"); id != "" {
		n.button(n.input.say(phrase{"👤 Customer", "👤 Клиент"}), "/admin/customers/"+url.PathEscape(id))
	}
}

// fieldLabel splits a camelCase key into lower-case words.
func fieldLabel(field string) string {
	builder := &strings.Builder{}
	for index, letter := range field {
		if index > 0 && letter >= 'A' && letter <= 'Z' {
			builder.WriteByte(' ')
		}
		builder.WriteRune(letter)
	}
	return humanize(strings.ToLower(builder.String()))
}

// topicTitles are the topic names in Russian. The forum topics keep the
// English names they were created with.
var topicTitlesRU = map[string]string{
	"purchase": "💳 Покупки", "renewal": "♻️ Продления", "topup": "👛 Пополнения", "refund": "↩️ Возвраты",
	"fulfillment_failure": "⚠️ Сбои выдачи", "incident": "🚨 Инциденты", "backup": "💾 Резервные копии",
	"security": "🔐 Безопасность", "support": "💬 Поддержка", "campaign_test": "📣 Превью рассылок",
	"notice_test": "🔔 Превью уведомлений", "node": "🖥 Ноды Remnawave", "monitoring": "📈 Мониторинг",
	"anomaly": "📈 Аномалии", "trial": "🎁 Пробные периоды", "addon": "➕ Дополнения", "promo": "🏷 Промокоды",
	"partner": "🤝 Партнёры", "error": "🐞 Ошибки", "infrastructure": "🛠 Инфраструктура", "report": "📊 Отчёты",
	"marketing": "🎯 Лояльность и рефералы", "receipt": "🧾 Чеки самозанятого",
}

func topicTitle(kind, locale string) string {
	if locale == "ru" {
		if title := topicTitlesRU[kind]; title != "" {
			return title
		}
	}
	if title := topicNames[kind]; title != "" {
		return title
	}
	return humanize(kind)
}
