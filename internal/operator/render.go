package operator

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"
)

// Readable notices.
//
// A notice is read in a chat by somebody deciding whether to act, so it is
// laid out the way the previous bot laid it out: a short header in capitals,
// then one line per fact with an emoji and a bold label, the time, and the
// buttons that make sense for the event — the customer's Telegram, their card
// in the panel, the order, and the few actions that are safe to take from a
// chat. Plain HTML only: bold labels, emoji, and line breaks.
//
// What a notice may say: identifiers, amounts, plan names, the customer's
// Telegram name and handle, and — for a support ticket, unless the support
// section turns it off — the subject and the customer's message, because the
// person reading the group is the person who will answer it. Never an
// operator's address, a token, a subscription link, or a provider payload.

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
	FirstName  string
	LastName   string
	// Status is the customer's account status: "active", "suspended", …
	Status string
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
	Subject    string
	CreatedAt  time.Time
	// Message is the customer's message the notice is about: the opening one
	// for a new ticket, the reply for a reply.
	Message string
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
	// Actions adds the buttons that act from the chat (close a ticket, block
	// a customer, retry a delivery). Only the bot, which answers them, sets it.
	Actions bool
	// HideTicketText leaves the ticket's subject and message out.
	HideTicketText bool
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
	input renderInput
	// title is HTML-escaped text; finish writes it in capitals.
	title string
	// keepCase leaves the title as written, for a header that carries a
	// product name and a version.
	keepCase bool
	// subtitle is a plain line under the title.
	subtitle string
	lines    []string
	// stamp labels the time line; "Time" when empty.
	stamp phrase
	// at overrides the notice's time, e.g. with the ticket's creation time.
	at time.Time
	// links are URL buttons, two to a row; actions are callback buttons, one
	// to a row, under the links.
	links   []models.InlineKeyboardButton
	actions []models.InlineKeyboardButton
}

// field writes "emoji <b>Label:</b> value". An empty value writes nothing.
func (n *notice) field(emoji string, label phrase, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	n.lines = append(n.lines, emoji+" <b>"+n.input.say(label)+":</b> "+value)
}

// block writes a label on its own line with the value under it, for text
// longer than a line.
func (n *notice) block(emoji string, label phrase, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	n.lines = append(n.lines, emoji+" <b>"+n.input.say(label)+":</b>\n"+value)
}

// line writes an unlabelled line.
func (n *notice) line(emoji, text string) {
	if strings.TrimSpace(text) != "" {
		n.lines = append(n.lines, emoji+" "+text)
	}
}

// button links a page of the panel. It is left out when the panel has no
// HTTPS address, since Telegram refuses anything else.
func (n *notice) button(text, path string) {
	base := strings.TrimSuffix(strings.TrimSpace(n.input.PanelURL), "/")
	if !strings.HasPrefix(base, "https://") || path == "" {
		return
	}
	n.link(text, base+path)
}

// link adds a URL button to any HTTPS address, once.
func (n *notice) link(text, target string) {
	if !strings.HasPrefix(target, "https://") {
		return
	}
	for _, existing := range n.links {
		if existing.URL == target {
			return
		}
	}
	n.links = append(n.links, models.InlineKeyboardButton{Text: text, URL: target})
}

// action adds a button the bot answers in the chat.
func (n *notice) action(text, action, id string) {
	if !n.input.Actions || id == "" {
		return
	}
	n.actions = append(n.actions, models.InlineKeyboardButton{Text: text, CallbackData: ActionData(action, id)})
}

func (n *notice) finish() (string, *models.InlineKeyboardMarkup) {
	builder := &strings.Builder{}
	title := n.title
	if !n.keepCase {
		title = upperText(title)
	}
	builder.WriteString("<b>" + title + "</b>\n")
	if n.subtitle != "" {
		builder.WriteString(n.subtitle + "\n")
	}
	builder.WriteString("\n")
	for _, line := range n.lines {
		builder.WriteString(line + "\n")
	}
	stamp := n.stamp
	if stamp.en == "" {
		stamp = phrase{"Time", "Время"}
	}
	input := n.input
	if !n.at.IsZero() {
		input.At = n.at
	}
	builder.WriteString("📅 <b>" + n.input.say(stamp) + ":</b> " + input.when())
	return builder.String(), noticeMarkup(n.links, n.actions)
}

// noticeMarkup lays the buttons out: links two to a row, then each action on
// a row of its own, the way the previous bot's notices looked.
func noticeMarkup(links, actions []models.InlineKeyboardButton) *models.InlineKeyboardMarkup {
	if len(links) == 0 && len(actions) == 0 {
		return nil
	}
	rows := [][]models.InlineKeyboardButton{}
	for start := 0; start < len(links); start += 2 {
		rows = append(rows, links[start:min(start+2, len(links))])
	}
	for _, action := range actions {
		rows = append(rows, []models.InlineKeyboardButton{action})
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// upperText capitalises escaped HTML text without touching its entities:
// "&amp;" stays "&amp;", which Telegram would not read as "&AMP;".
func upperText(text string) string {
	builder := &strings.Builder{}
	inEntity := false
	for _, letter := range text {
		switch {
		case letter == '&':
			inEntity = true
		case inEntity && letter == ';':
			inEntity = false
		case !inEntity:
			letter = unicode.ToUpper(letter)
		}
		builder.WriteRune(letter)
	}
	return builder.String()
}

// telegramUsername is what Telegram accepts as a public username.
var telegramUsername = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{3,31}$`)

// customer writes who the notice is about — their Telegram name, ID, and
// handle — and links their Telegram and their card in the panel.
func (n *notice) customer(customerID string) {
	facts := n.input.Facts.Customer
	if facts != nil && facts.ID != "" {
		customerID = facts.ID
	}
	if facts == nil && customerID == "" {
		return
	}
	name := ""
	if facts != nil {
		name = strings.TrimSpace(facts.FirstName + " " + facts.LastName)
	}
	switch {
	case name != "":
		n.field("👤", phrase{"Customer", "Пользователь"}, html.EscapeString(name))
	case facts == nil || facts.TelegramID == 0 && facts.Username == "":
		n.field("👤", phrase{"Customer", "Пользователь"}, code(shortID(customerID)))
	}
	if facts != nil && facts.TelegramID != 0 {
		n.field("🆔", phrase{"Telegram ID", "Telegram ID"}, code(strconv.FormatInt(facts.TelegramID, 10)))
	}
	if facts != nil && facts.Username != "" {
		n.field("📱", phrase{"Username", "Username"}, "@"+html.EscapeString(facts.Username))
		if telegramUsername.MatchString(facts.Username) {
			n.link(n.input.say(phrase{"✉️ DM", "✉️ ЛС"}), "https://t.me/"+facts.Username)
		}
	}
	if customerID != "" {
		n.button(n.input.say(phrase{"👤 Profile", "👤 Профиль"}), "/admin/customers/"+url.PathEscape(customerID))
	}
}

// blockAction offers to block the customer, or to unblock one who is.
func (n *notice) blockAction(customerID string) {
	facts := n.input.Facts.Customer
	if facts != nil && facts.ID != "" {
		customerID = facts.ID
	}
	if facts != nil && facts.Status == "suspended" {
		n.action(n.input.say(phrase{"✅ Unblock", "✅ Разблокировать"}), ActionUnblock, customerID)
		return
	}
	n.action(n.input.say(phrase{"🚫 Block", "🚫 Заблокировать"}), ActionBlock, customerID)
}

func (n *notice) orderButton(orderID string) {
	if orderID != "" {
		n.button(n.input.say(phrase{"🧾 Order", "🧾 Заказ"}), "/admin/finance/"+url.PathEscape(orderID))
	}
}

func (n *notice) orderField(orderID string) {
	if orderID != "" {
		n.field("🧾", phrase{"Order", "Заказ"}, code(shortID(orderID)))
	}
}

// orderLines writes what an order bought and how it was paid.
func (n *notice) orderLines(order *orderFacts, amountMinor int64, currency string) {
	if order != nil {
		if plan := html.EscapeString(order.PlanName); plan != "" {
			if period := n.input.period(order.PeriodSeconds); period != "" {
				plan += " · " + period
			}
			n.field("📦", phrase{"Plan", "Тариф"}, plan)
		}
		if currency == "" {
			currency = order.Currency
		}
		if amountMinor == 0 {
			amountMinor = order.PaidMinor
		}
	}
	if amountMinor > 0 && currency != "" {
		n.field("💰", phrase{"Paid", "Оплачено"}, "<b>"+formatMoney(amountMinor, currency)+"</b>")
	}
	if order == nil {
		return
	}
	if order.Provider != "" {
		n.field("💳", phrase{"Method", "Способ"}, n.input.via(order.Provider, order.Method))
	}
	if order.WalletMinor > 0 {
		n.field("👛", phrase{"From the wallet", "С баланса"}, formatMoney(order.WalletMinor, order.Currency))
	}
	if order.DiscountMinor > 0 {
		discount := "−" + formatMoney(order.DiscountMinor, order.Currency)
		if order.PromoCode != "" {
			discount += " · " + n.input.say(phrase{"promo", "промокод"}) + " " + code(order.PromoCode)
		}
		n.field("🏷", phrase{"Discount", "Скидка"}, discount)
	}
}

func (n *notice) paymentNumber() {
	if facts := n.input.Facts.Customer; facts != nil && facts.Payments > 0 {
		emoji := "🔁"
		if facts.Payments == 1 {
			emoji = "🆕"
		}
		n.field(emoji, phrase{"Payment", "Оплата"}, n.input.paymentCount(facts.Payments))
	}
}

// walletBalance writes the balance after a credit: "250 ₽ → 750 ₽" when the
// balance before can be told, the balance now otherwise.
func (n *notice) walletBalance(credited int64, currency string) {
	facts := n.input.Facts.Customer
	if facts == nil || facts.WalletCurrency == "" {
		return
	}
	now := "<b>" + formatMoney(facts.WalletMinor, facts.WalletCurrency) + "</b>"
	before := facts.WalletMinor - credited
	if credited > 0 && strings.EqualFold(currency, facts.WalletCurrency) && before >= 0 {
		n.field("💼", phrase{"Balance", "Баланс"}, formatMoney(before, facts.WalletCurrency)+" → "+now)
		return
	}
	n.field("💼", phrase{"Balance now", "Баланс сейчас"}, now)
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
	if customerID == "" && input.Facts.Customer != nil {
		customerID = input.Facts.Customer.ID
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
		n.orderField(orderID)
		n.orderButton(orderID)
	case "promo":
		n.title = input.say(phrase{"🏷 Promo code used", "🏷 Применён промокод"})
		n.customer(customerID)
		if order := input.Facts.Order; order != nil && order.PromoCode != "" {
			n.field("🎟", phrase{"Code", "Код"}, code(order.PromoCode))
		}
		if amount > 0 {
			discountCurrency := currency
			if discountCurrency == "" && input.Facts.Order != nil {
				discountCurrency = input.Facts.Order.Currency
			}
			n.field("💸", phrase{"Discount", "Скидка"}, "<b>−"+formatMoney(amount, discountCurrency)+"</b>")
		}
		n.orderField(orderID)
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
	n.orderField(orderID)
	n.orderButton(orderID)
}

func (n *notice) renderTopUp(event, orderID, customerID, currency string) {
	credited, _ := n.input.num("creditedMinor")
	if credited == 0 {
		credited, _ = n.input.num("amountMinor")
	}
	switch event {
	case "bedolaga_payment_credited":
		n.title = n.input.say(phrase{"💰 Old bot's payment credited", "💰 Зачислен платёж из старого бота"})
	case "bedolaga_payment_needs_review":
		n.title = n.input.say(phrase{"⚠️ Old bot's payment needs a decision", "⚠️ Платёж из старого бота ждёт решения"})
	default:
		switch n.input.str("classification") {
		case "late":
			n.title = n.input.say(phrase{"💰 Late top-up credited", "💰 Зачислено запоздавшее пополнение"})
		case "reconciled":
			n.title = n.input.say(phrase{"💰 Top-up found by reconciliation", "💰 Пополнение найдено сверкой"})
		default:
			n.title = n.input.say(phrase{"💰 Wallet top-up", "💰 Пополнение баланса"})
		}
	}
	n.customer(customerID)
	if credited != 0 && currency != "" {
		n.field("💵", phrase{"Amount", "Сумма"}, "<b>+"+formatMoney(credited, currency)+"</b>")
	}
	if provider := n.input.str("provider"); provider != "" {
		n.field("💳", phrase{"Method", "Способ"}, html.EscapeString(providerName(provider)))
	} else if order := n.input.Facts.Order; order != nil && order.Provider != "" {
		n.field("💳", phrase{"Method", "Способ"}, n.input.via(order.Provider, order.Method))
	}
	if event != "bedolaga_payment_needs_review" {
		n.walletBalance(credited, currency)
	}
	if reason := n.input.str("reason"); reason != "" {
		n.field("❓", phrase{"Why", "Причина"}, reasonText(n.input, reason))
	}
	if reference := n.input.str("providerReference"); reference != "" {
		n.field("🔖", phrase{"Payment ID", "ID платежа"}, code(reference))
	}
	if event == "" {
		n.paymentNumber()
	}
	n.orderField(orderID)
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
		value := "<b>−" + formatMoney(amount, currency) + "</b>"
		if state, ok := refundStates[status]; ok {
			value += " · " + n.input.say(state)
		}
		n.field("💸", phrase{"Amount", "Сумма"}, value)
	}
	if order := n.input.Facts.Order; order != nil && order.PlanName != "" {
		n.field("📦", phrase{"Plan", "Тариф"}, html.EscapeString(order.PlanName))
	}
	if operation := n.input.str("operation"); operation != "" {
		if title, ok := saleTitles[operation]; ok {
			n.field("🧾", phrase{"For", "За"}, n.input.say(title))
		}
	}
	if wallet := n.input.str("wallet"); wallet != "" {
		n.field("👛", phrase{"Wallet", "Баланс"}, html.EscapeString(humanize(wallet)))
	}
	if reason := n.input.str("reason"); reason != "" {
		n.field("❓", phrase{"Why", "Причина"}, reasonText(n.input, reason))
	}
	if errorCode := n.input.str("errorCode"); errorCode != "" {
		n.field("🧩", phrase{"Error", "Ошибка"}, code(errorCode))
	}
	n.orderField(orderID)
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
			n.field("⚙️", phrase{"How", "Как"}, reasonText(n.input, source))
		}
		if reason := n.input.str("reason"); reason != "" {
			n.field("📝", phrase{"Reason", "Причина"}, html.EscapeString(reason))
		}
		return
	}
	if customerID != "" || n.input.Facts.Customer != nil {
		n.customer(customerID)
	}
	if amount > 0 && currency != "" {
		n.field("💰", phrase{"Amount", "Сумма"}, "<b>"+formatMoney(amount, currency)+"</b>")
	}
	if provider := n.input.str("provider"); provider != "" {
		n.field("🏦", phrase{"Provider", "Провайдер"}, html.EscapeString(providerName(provider)))
	}
	if classification := n.input.str("classification"); classification != "" {
		if text, ok := classifications[classification]; ok {
			n.field("🔎", phrase{"What", "Что"}, n.input.say(text))
		} else {
			n.field("🔎", phrase{"What", "Что"}, html.EscapeString(humanize(classification)))
		}
	}
	if reason := n.input.str("reason"); reason != "" {
		n.field("❓", phrase{"Why", "Причина"}, reasonText(n.input, reason))
	}
	if errorCode := n.input.str("errorCode"); errorCode != "" {
		n.field("🧩", phrase{"Error", "Ошибка"}, code(errorCode))
	}
	n.orderField(orderID)
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
			n.field("🔧", phrase{"Step", "Шаг"}, n.input.say(text))
		} else {
			n.field("🔧", phrase{"Step", "Шаг"}, html.EscapeString(humanize(operation)))
		}
	}
	if paid, ok := n.input.num("paidMinor"); ok && paid > 0 {
		n.field("💰", phrase{"Paid", "Оплачено"}, formatMoney(paid, n.input.str("currency")))
	}
	if provider := n.input.str("provider"); provider != "" {
		n.field("🏦", phrase{"Provider", "Провайдер"}, html.EscapeString(providerName(provider)))
	}
	if reason := n.input.str("reason"); reason != "" {
		n.field("❓", phrase{"Why", "Причина"}, reasonText(n.input, reason))
	}
	if errorCode := n.input.str("errorCode"); errorCode != "" {
		n.field("🧩", phrase{"Error", "Ошибка"}, code(errorCode))
	}
	if status == "retrying" {
		n.field("🔁", phrase{"Status", "Статус"}, n.input.say(phrase{"retrying automatically", "повторяется автоматически"}))
	}
	n.orderField(orderID)
	n.orderButton(orderID)
	n.button(n.input.say(phrase{"🛠 Deliveries", "🛠 Выдачи"}), "/admin/system")
	if operation := n.input.str("operationId"); operation != "" && status == "retrying" {
		n.action(n.input.say(phrase{"🔁 Retry now", "🔁 Повторить сейчас"}), ActionRetryFulfillment, operation)
	}
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
		n.field("📄", phrase{"File", "Файл"}, code(file))
	}
	if size, ok := n.input.num("sizeBytes"); ok && size > 0 {
		n.field("📦", phrase{"Size", "Размер"}, bytesText(size))
	}
	if source := n.input.str("source"); source != "" {
		n.field("⚙️", phrase{"Source", "Источник"}, html.EscapeString(humanize(source)))
	}
	n.button(n.input.say(phrase{"💾 Backups", "💾 Копии"}), "/admin/settings/operations")
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
	if admin := n.input.str("adminUserId"); admin != "" {
		n.field("👮", phrase{"Operator", "Оператор"}, code(shortID(admin)))
		n.button(n.input.say(phrase{"👮 Operators", "👮 Операторы"}), "/admin/operators")
	}
	if method := n.input.str("method"); method != "" {
		if text, ok := methodNames[method]; ok {
			n.field("🧭", phrase{"How", "Как"}, n.input.say(text))
		} else {
			n.field("🧭", phrase{"How", "Как"}, html.EscapeString(humanize(method)))
		}
	}
	if event == "customer_deletion_requested" {
		n.customer(customerID)
		n.field("⏳", phrase{"Status", "Статус"}, n.input.say(phrase{"waiting for an operator", "ждёт оператора"}))
	}
	n.button(n.input.say(phrase{"📜 Audit log", "📜 Журнал аудита"}), "/admin/audit")
}

var priorityNames = map[string]phrase{
	"low": {"low", "низкий"}, "normal": {"normal", "обычный"}, "high": {"high", "высокий"}, "urgent": {"🔥 urgent", "🔥 срочный"},
}

// ticketTextLimit bounds the message a ticket notice quotes. The rest is one
// tap away in the panel.
const ticketTextLimit = 800

func clip(text string, limit int) string {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return strings.TrimSpace(string(runes[:limit])) + "…"
}

func (n *notice) renderSupport(event, customerID string) {
	ticket := n.input.Facts.Ticket
	switch event {
	case "customer_reply":
		n.title = n.input.say(phrase{"💬 Customer replied", "💬 Ответ в тикете"})
	case "sla_breach":
		n.title = n.input.say(phrase{"⏰ First reply is overdue", "⏰ Просрочен первый ответ"})
	default:
		n.title = n.input.say(phrase{"🎫 New ticket", "🎫 Новый тикет"})
		n.stamp = phrase{"Created", "Создан"}
		if ticket != nil && !ticket.CreatedAt.IsZero() {
			n.at = ticket.CreatedAt
		}
	}
	ticketID := n.input.str("ticketId")
	if ticketID == "" && ticket != nil {
		ticketID = ticket.ID
	}
	if ticketID != "" {
		n.field("🆔", phrase{"ID", "ID"}, code(shortID(ticketID)))
	}
	n.customer(customerID)
	if ticket != nil && ticket.QueueName != "" {
		n.field("📂", phrase{"Queue", "Очередь"}, html.EscapeString(ticket.QueueName))
	}
	priority := n.input.str("priority")
	if priority == "" && ticket != nil {
		priority = ticket.Priority
	}
	if text, ok := priorityNames[priority]; ok && priority != "normal" {
		n.field("⚡", phrase{"Priority", "Приоритет"}, n.input.say(text))
	}
	if overdue, ok := n.input.num("overdueMinutes"); ok && overdue > 0 {
		n.field("⏱", phrase{"Overdue by", "Просрочено на"}, "<b>"+minutesText(n.input, overdue)+"</b>")
	}
	if ticket != nil && !n.input.HideTicketText {
		n.field("📝", phrase{"Subject", "Заголовок"}, html.EscapeString(ticket.Subject))
		if event != "sla_breach" {
			n.block("📩", phrase{"Message", "Сообщение"}, html.EscapeString(clip(ticket.Message, ticketTextLimit)))
		}
	}
	if ticketID != "" {
		n.button(n.input.say(phrase{"💬 Open the ticket", "💬 Открыть тикет"}), "/admin/support?ticketId="+url.QueryEscape(ticketID))
		if ticket == nil || ticket.Status != "closed" && ticket.Status != "resolved" && ticket.Status != "merged" {
			n.action(n.input.say(phrase{"🔒 Close the ticket", "🔒 Закрыть тикет"}), ActionCloseTicket, ticketID)
		}
	}
	if customerID != "" {
		n.blockAction(customerID)
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
	if observed := n.input.str("observed"); observed != "" {
		n.field("📊", phrase{"Observed", "Сейчас"}, "<b>"+html.EscapeString(observed)+"</b>")
	}
	if threshold := n.input.str("threshold"); threshold != "" {
		n.field("📏", phrase{"Threshold", "Порог"}, html.EscapeString(threshold))
	}
	if subject := n.input.str("subjectType"); subject != "" {
		n.field("🎯", phrase{"Subject", "Объект"}, html.EscapeString(humanize(subject)))
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
		n.field("💰", phrase{"Reward", "Награда"}, "<b>+"+formatMoney(amount, currency)+"</b>")
	}
	if order := n.input.Facts.Order; order != nil && order.PaidMinor > 0 {
		n.field("🧾", phrase{"For an order of", "За заказ на"}, formatMoney(order.PaidMinor, order.Currency))
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
	n.field("🔀", phrase{"Tier", "Уровень"}, code(from)+" → "+code(to))
	if reason := n.input.str("reason"); reason != "" {
		n.field("❓", phrase{"Why", "Причина"}, reasonText(n.input, reason))
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
	n.field("📡", phrase{"Event", "Событие"}, html.EscapeString(humanize(strings.TrimPrefix(event, "node."))))
	if country := n.input.str("country"); country != "" {
		n.field(firstNonEmpty(countryFlag(country), "🌍"), phrase{"Country", "Страна"}, html.EscapeString(strings.ToUpper(country)))
	}
	if provider := n.input.str("provider"); provider != "" {
		n.field("🏢", phrase{"Hosting", "Хостинг"}, html.EscapeString(provider))
	}
	n.button(n.input.say(phrase{"🖥 System", "🖥 Система"}), "/admin/system")
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
			n.field("👥", phrase{"Customers", "Клиентов"}, "<b>"+html.EscapeString(users)+"</b>")
		}
		if devices := n.input.str("devices"); devices != "" {
			n.block("📋", phrase{"Who", "Кто"}, html.EscapeString(devices))
		}
	case strings.Contains(strings.ToLower(event), "torrent"):
		n.title = n.input.say(phrase{"🏴‍☠️ Torrent traffic blocked", "🏴‍☠️ Заблокирован торрент-трафик"})
		n.customer(customerID)
		if node := n.input.str("node"); node != "" {
			n.field("🖥", phrase{"Node", "Нода"}, html.EscapeString(node))
		}
		if customerID != "" {
			n.blockAction(customerID)
		}
	default:
		n.title = "📈 " + html.EscapeString(humanize(event))
		n.customer(customerID)
	}
	if user := n.input.str("remnawaveUserId"); user != "" {
		n.field("🆔", phrase{"Remnawave", "Remnawave"}, code(user))
	}
}

func (n *notice) renderError() {
	process := n.input.str("process")
	n.title = "🐞 " + n.input.say(phrase{"Error in", "Ошибка в"}) + " " + html.EscapeString(firstNonEmpty(process, "?"))
	if occurrences, ok := n.input.num("occurrences"); ok {
		if occurrences <= 1 {
			n.field("🆕", phrase{"Seen", "Повторов"}, n.input.say(phrase{"first time", "впервые"}))
		} else {
			n.field("🔁", phrase{"Seen", "Повторов"}, "<b>"+groupDigits(occurrences)+"</b>")
		}
	}
	if fingerprint := n.input.str("fingerprint"); fingerprint != "" {
		n.field("🧬", phrase{"Fingerprint", "Отпечаток"}, code(fingerprint))
	}
	n.button(n.input.say(phrase{"🛠 Error log", "🛠 Журнал ошибок"}), "/admin/system")
}

func (n *notice) renderInfrastructure(event string) {
	version := n.input.str("version")
	switch {
	case event == "update_available":
		n.title = n.input.say(phrase{"🆕 A new version is out", "🆕 Вышла новая версия"})
		n.field("📦", phrase{"Version", "Версия"}, code(n.input.str("previousVersion"))+" → <b>"+html.EscapeString(version)+"</b>")
		if release := n.input.str("releaseUrl"); release != "" {
			n.link(n.input.say(phrase{"📝 What changed", "📝 Что нового"}), release)
		}
	case event == "bot_started" && n.input.Payload["report"] != nil:
		n.renderStartReport()
	case strings.HasSuffix(event, "_started"):
		process := strings.TrimSuffix(event, "_started")
		n.title = "🚀 " + html.EscapeString(firstNonEmpty(process, n.input.str("process"))) + " " + n.input.say(phrase{"started", "запущен"})
		if version != "" {
			n.field("🏷", phrase{"Version", "Версия"}, "<b>"+html.EscapeString(version)+"</b>")
		}
	default:
		n.title = "🛠 " + html.EscapeString(humanize(event))
		if version != "" {
			n.field("🏷", phrase{"Version", "Версия"}, html.EscapeString(version))
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
		n.field("💰", phrase{"Amount", "Сумма"}, "<b>"+formatMoney(amount, firstNonEmpty(currency, "RUB"))+"</b>")
	}
	if receipt := n.input.str("receiptUrl"); receipt != "" {
		n.link(n.input.say(phrase{"🧾 Receipt", "🧾 Чек"}), receipt)
	}
	n.orderField(orderID)
	n.orderButton(orderID)
}

// renderFields is the fallback for a notice no template knows: its topic as
// the title and its allowlisted fields in words.
func (n *notice) renderFields() {
	if n.title == "" {
		n.title = html.EscapeString(topicHeading(n.input.Kind, n.input.Locale))
	}
	n.lines = nil
	for _, field := range notificationFields {
		if value := n.input.str(field); value != "" {
			n.field("▫️", phrase{fieldLabel(field), ""}, code(value))
		}
	}
	if id := n.input.str("orderId"); id != "" {
		n.orderButton(id)
	}
	if id := n.input.str("customerId"); id != "" {
		n.button(n.input.say(phrase{"👤 Profile", "👤 Профиль"}), "/admin/customers/"+url.PathEscape(id))
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
	return html.EscapeString(humanize(strings.ToLower(builder.String())))
}
