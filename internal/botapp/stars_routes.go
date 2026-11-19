package botapp

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/goods"
)

// The Stars flow: the fork's «⭐ Купить звёзды», on top of the same order,
// wallet, and delivery pipeline every shop purchase uses.
//
// It runs recipient first (yourself, or a typed @username), then the amount
// (preset buttons or a typed number), then one confirmation that restates the
// recipient, the amount, the total, and the balance above the button that
// pays. A wallet that covers the total pays at once. One that does not saves
// the purchase and offers to top up exactly the shortfall; for a product the
// operator priced themselves, the purchase then makes itself when the top-up
// settles, at the price the customer confirmed — and is refused, with the
// customer told, if the price rose in between.

// routeStars opens the Stars flow from the main menu.
const routeStars = "stars"

// starsActions is the closed set of callback actions the Stars flow owns. It is
// folded into shopActions, which is what lets a tap reach handleStarsAction.
var starsActions = map[string]bool{
	"stars-self": true, "stars-other": true, "stars-amt": true, "stars-qty": true,
	"stars-custom": true, "stars-buy": true,
}

func init() {
	for action := range starsActions {
		shopActions[action] = true
	}
	for key, message := range starsCatalog {
		catalog[key] = message
	}
}

// starsProduct is the product the Stars flow sells: the first visible
// custom-amount Stars product. An installation that sells only fixed packs has
// none, and the flow falls back to the shop catalogue.
func (app *App) starsProduct(ctx context.Context, locale Locale) (ShopProduct, bool, error) {
	products, err := app.customers.ShopProducts(ctx, locale)
	if err != nil {
		return ShopProduct{}, false, err
	}
	for _, product := range products {
		if product.CustomAmount() {
			return product, true, nil
		}
	}
	return ShopProduct{}, false, nil
}

// handleStarsAction serves the Stars flow's buttons. It reports false for an
// action it does not own.
func (app *App) handleStarsAction(ctx context.Context, session commerceContext, parts []string) (View, bool) {
	if !starsActions[parts[0]] {
		return View{}, false
	}
	argument := func(index int) string {
		if len(parts) > index {
			return parts[index]
		}
		return ""
	}
	switch parts[0] {
	case "stars-self":
		return app.starsForSelf(ctx, session), true
	case "stars-other":
		return app.starsAskRecipient(ctx, session), true
	}

	review, err := app.customers.ShopReviewFor(ctx, session.Customer.ID, argument(1))
	if err != nil {
		if !errors.Is(err, ErrShopReviewGone) {
			app.logger.Error("stars review lookup failed", "error", err)
		}
		return app.starsExpired(session), true
	}
	stars, _ := strconv.Atoi(argument(2))
	switch parts[0] {
	case "stars-amt":
		return app.starsAmountScreen(ctx, session, review), true
	case "stars-custom":
		return app.starsAskAmount(ctx, session, review), true
	case "stars-qty":
		return app.starsConfirmScreen(ctx, session, review, stars, ""), true
	default:
		return app.starsBuy(ctx, session, review, stars), true
	}
}

// starsScreen is the entry: the price per Star, the bounds, and who to send to.
func (app *App) starsScreen(ctx context.Context, session commerceContext) View {
	product, found, err := app.starsProduct(ctx, session.Locale)
	if err != nil {
		app.logger.Error("stars product lookup failed", "error", err)
		return app.errorView(session.Locale, routeHome)
	}
	if !found {
		return app.shopScreen(ctx, session)
	}
	body := &strings.Builder{}
	body.WriteString(text(session.Locale, "stars.title"))
	body.WriteString("\n\n")
	if product.UnitMinor != nil {
		body.WriteString(text(session.Locale, "stars.unitPrice",
			formatMoney(*product.UnitMinor, product.Currency), product.MinStars, product.MaxStars))
	} else {
		body.WriteString(text(session.Locale, "stars.bounds", product.MinStars, product.MaxStars))
	}
	body.WriteString("\n\n")
	body.WriteString(text(session.Locale, "stars.who"))

	rows := make([][]models.InlineKeyboardButton, 0, 4)
	if session.Username != "" {
		rows = append(rows, row(actionButton(text(session.Locale, "stars.toSelf", session.Username), "stars-self")))
	}
	rows = append(rows,
		row(actionButton(text(session.Locale, "stars.toOther"), "stars-other")),
		row(callbackButton(text(session.Locale, "stars.myOrders"), routeShopOrders)),
		row(callbackButton(text(session.Locale, "action.back"), routeHome)),
	)
	return View{Text: body.String(), Keyboard: keyboard(rows...)}
}

func (app *App) starsExpired(session commerceContext) View {
	return View{
		Text:     text(session.Locale, "stars.expired"),
		Keyboard: keyboard(row(callbackButton(text(session.Locale, "stars.restart"), routeStars))),
	}
}

func (app *App) starsForSelf(ctx context.Context, session commerceContext) View {
	if session.Username == "" {
		return app.starsAskRecipient(ctx, session)
	}
	return app.starsWithRecipient(ctx, session, session.Username)
}

func (app *App) starsAskRecipient(ctx context.Context, session commerceContext) View {
	product, found, err := app.starsProduct(ctx, session.Locale)
	if err != nil || !found {
		return app.starsScreen(ctx, session)
	}
	if err := app.customers.BeginSessionState(ctx, session.TelegramID, "goods_recipient",
		map[string]any{"productId": product.ID, "flow": "stars"}); err != nil {
		app.logger.Error("stars recipient prompt failed", "error", err)
		return app.errorView(session.Locale, routeStars)
	}
	return View{
		Text:     text(session.Locale, "stars.askRecipient"),
		Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routeStars))),
	}
}

// SubmitStarsRecipient handles the username typed in the Stars flow.
func (app *App) SubmitStarsRecipient(ctx context.Context, session commerceContext, input string) View {
	recipient, err := goods.NormalizeRecipient(input)
	if err != nil {
		return View{
			Text:     text(session.Locale, "stars.badRecipient"),
			Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routeStars))),
		}
	}
	return app.starsWithRecipient(ctx, session, recipient)
}

// starsWithRecipient records the chosen recipient as a review — the same
// short-lived row the shop's confirmation step uses — so every later button
// carries its identifier rather than the handle, which keeps callback data
// under Telegram's 64 bytes.
func (app *App) starsWithRecipient(ctx context.Context, session commerceContext, recipient string) View {
	product, found, err := app.starsProduct(ctx, session.Locale)
	if err != nil || !found {
		return app.starsScreen(ctx, session)
	}
	reviewID, err := app.customers.OpenShopReview(ctx, session.Customer.ID, product.ID, recipient)
	if err != nil {
		app.logger.Error("stars review could not be recorded", "error", err)
		return app.errorView(session.Locale, routeStars)
	}
	return app.starsAmountScreen(ctx, session, ShopReview{ID: reviewID, ProductID: product.ID, Recipient: recipient})
}

// starsAmountScreen offers the presets, each with its price, and a typed amount.
func (app *App) starsAmountScreen(ctx context.Context, session commerceContext, review ShopReview) View {
	product, found, err := app.customers.ShopProduct(ctx, session.Locale, review.ProductID)
	if err != nil || !found || !product.CustomAmount() {
		return app.starsExpired(session)
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(product.Presets)/2+3)
	current := make([]models.InlineKeyboardButton, 0, 2)
	for _, preset := range product.Presets {
		label := fmt.Sprintf("%d ⭐", preset)
		if quote, quoteErr := app.commerce.QuoteStars(ctx, product, 1, preset); quoteErr == nil {
			label = text(session.Locale, "stars.preset", preset, formatMoney(quote.PriceMinor, quote.Currency))
		}
		current = append(current, actionButton(label, "stars-qty:"+review.ID+":"+strconv.Itoa(preset)))
		if len(current) == 2 {
			rows = append(rows, current)
			current = make([]models.InlineKeyboardButton, 0, 2)
		}
	}
	if len(current) > 0 {
		rows = append(rows, current)
	}
	rows = append(rows,
		row(actionButton(text(session.Locale, "stars.customAmount"), "stars-custom:"+review.ID)),
		row(callbackButton(text(session.Locale, "action.back"), routeStars)),
	)
	return View{
		Text:     text(session.Locale, "stars.askAmount", html.EscapeString(review.Recipient)),
		Keyboard: keyboard(rows...),
	}
}

func (app *App) starsAskAmount(ctx context.Context, session commerceContext, review ShopReview) View {
	product, found, err := app.customers.ShopProduct(ctx, session.Locale, review.ProductID)
	if err != nil || !found || !product.CustomAmount() {
		return app.starsExpired(session)
	}
	if err := app.customers.BeginSessionState(ctx, session.TelegramID, "goods_stars_amount",
		map[string]any{"reviewId": review.ID}); err != nil {
		app.logger.Error("stars amount prompt failed", "error", err)
		return app.errorView(session.Locale, routeStars)
	}
	return View{
		Text:     text(session.Locale, "stars.askCustom", product.MinStars, product.MaxStars),
		Keyboard: keyboard(row(actionButton(text(session.Locale, "action.back"), "stars-amt:"+review.ID))),
	}
}

// SubmitStarsAmount handles a typed amount.
func (app *App) SubmitStarsAmount(ctx context.Context, session commerceContext, reviewID, input string) View {
	review, err := app.customers.ShopReviewFor(ctx, session.Customer.ID, reviewID)
	if err != nil {
		return app.starsExpired(session)
	}
	product, found, err := app.customers.ShopProduct(ctx, session.Locale, review.ProductID)
	if err != nil || !found || !product.CustomAmount() {
		return app.starsExpired(session)
	}
	stars, err := strconv.Atoi(strings.ReplaceAll(strings.TrimSpace(input), " ", ""))
	if err != nil || stars < product.MinStars || stars > product.MaxStars {
		return View{
			Text: text(session.Locale, "stars.badAmount", product.MinStars, product.MaxStars),
			Keyboard: keyboard(
				row(actionButton(text(session.Locale, "stars.customAmount"), "stars-custom:"+review.ID)),
				row(actionButton(text(session.Locale, "action.back"), "stars-amt:"+review.ID)),
			),
		}
	}
	return app.starsConfirmScreen(ctx, session, review, stars, "")
}

// starsConfirmScreen restates everything above the button that pays.
func (app *App) starsConfirmScreen(
	ctx context.Context, session commerceContext, review ShopReview, stars int, notice string,
) View {
	product, found, err := app.customers.ShopProduct(ctx, session.Locale, review.ProductID)
	if err != nil || !found || !product.CustomAmount() {
		return app.starsExpired(session)
	}
	if stars < product.MinStars || stars > product.MaxStars {
		return app.starsAmountScreen(ctx, session, review)
	}
	quote, err := app.commerce.QuoteStars(ctx, product, 1, stars)
	if err != nil {
		app.logger.Warn("stars quote failed", "product", product.Code, "error", err)
		return shopItemView(session.Locale, product, ShopQuote{}, true)
	}
	balance, err := app.commerce.AvailableBalance(ctx, session.Customer.ID, quote.Currency)
	if err != nil {
		app.logger.Error("wallet balance lookup failed", "error", err)
		return app.errorView(session.Locale, routeStars)
	}
	body := &strings.Builder{}
	if notice != "" {
		body.WriteString(notice + "\n\n")
	}
	body.WriteString(text(session.Locale, "stars.confirm",
		html.EscapeString(review.Recipient), stars,
		formatMoney(quote.PriceMinor, quote.Currency), formatMoney(max(balance, 0), quote.Currency)))
	body.WriteString("\n\n")
	body.WriteString(text(session.Locale, "shop.irreversible"))
	return View{
		Text: body.String(),
		Keyboard: keyboard(
			row(actionButton(text(session.Locale, "stars.pay"), "stars-buy:"+review.ID+":"+strconv.Itoa(stars))),
			row(actionButton(text(session.Locale, "action.back"), "stars-amt:"+review.ID)),
		),
	}
}

// starsBuy pays from the wallet, or saves the purchase and offers to top up
// exactly what is missing.
func (app *App) starsBuy(ctx context.Context, session commerceContext, review ShopReview, stars int) View {
	product, found, err := app.customers.ShopProduct(ctx, session.Locale, review.ProductID)
	if err != nil || !found || !product.CustomAmount() || stars < product.MinStars || stars > product.MaxStars {
		return app.starsExpired(session)
	}
	if gate := app.checkPurchaseChannels(ctx, session.Customer.ID, session.TelegramID); !gate.Allowed() {
		return channelGateView(session.Locale, gate, "stars-buy:"+review.ID+":"+strconv.Itoa(stars))
	}
	quote, err := app.commerce.QuoteStars(ctx, product, 1, stars)
	if err != nil {
		return shopItemView(session.Locale, product, ShopQuote{}, true)
	}
	isSelf := strings.EqualFold(review.Recipient, session.Username)
	balance, err := app.commerce.AvailableBalance(ctx, session.Customer.ID, quote.Currency)
	if err != nil {
		app.logger.Error("wallet balance lookup failed", "error", err)
		return app.errorView(session.Locale, routeStars)
	}

	if balance < quote.PriceMinor {
		return app.starsCheckout(ctx, session, product, review, stars, isSelf, quote, max(balance, 0))
	}

	order, err := app.commerce.StartStarsPurchase(ctx, session.Customer.ID, product, stars,
		review.Recipient, isSelf, quote, "goods:stars:"+review.ID+":"+strconv.Itoa(stars), true)
	switch {
	case errors.Is(err, commercepg.ErrMaintenance):
		return app.maintenanceScreen(ctx, session)
	case errors.Is(err, commercepg.ErrPriceChanged):
		return app.starsConfirmScreen(ctx, session, review, stars, text(session.Locale, "stars.priceChanged"))
	case errors.Is(err, commercepg.ErrWalletShort):
		// Another purchase spent the balance between the check and the order.
		return app.starsCheckout(ctx, session, product, review, stars, isSelf, quote, 0)
	case err != nil:
		app.logger.Error("stars order creation failed", "error", err)
		return View{
			Text:     text(session.Locale, "error.order"),
			Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routeStars))),
		}
	}
	remaining, _ := app.commerce.AvailableBalance(ctx, session.Customer.ID, quote.Currency)
	return View{
		Text: text(session.Locale, "stars.paid", stars, html.EscapeString(review.Recipient),
			formatMoney(max(remaining, 0), quote.Currency)),
		Keyboard: keyboard(
			row(actionButton(text(session.Locale, "stars.viewOrder"), "shop-order:"+order.ID)),
			row(callbackButton(text(session.Locale, "stars.myOrders"), routeShopOrders),
				callbackButton(text(session.Locale, "action.menu"), routeHome)),
		),
	}
}

// starsCheckout is the fork's «К оплате N ₽»: the purchase is saved and the
// customer pays in exactly what is missing. The buttons are the ordinary
// top-up buttons for that amount, so every payment method the installation
// offers works, and nothing about the payment path is special to Stars.
func (app *App) starsCheckout(
	ctx context.Context, session commerceContext, product ShopProduct, review ShopReview,
	stars int, isSelf bool, quote ShopQuote, balance int64,
) View {
	limits := app.commerce.TopUpLimits()
	choices := app.commerce.ExternalPaymentChoices(quote.Currency)
	if len(choices) == 0 {
		return View{
			Text:     text(session.Locale, "pay.none"),
			Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routeStars))),
		}
	}
	if !product.OperatorPriced() || !limits.Enabled {
		// A provider quote is never charged unattended, and an installation
		// without top-ups has nothing to top up with. The ordinary shop path —
		// an order whose remainder a payment settles — still applies.
		return app.starsPayRemainder(ctx, session, product, review, stars, isSelf, quote, choices[0].Provider)
	}
	if err := app.commerce.SaveStarsForTopUp(ctx, session.Customer.ID, product, stars,
		review.Recipient, isSelf, quote); err != nil {
		app.logger.Error("stars cart save failed", "error", err)
		return app.errorView(session.Locale, routeStars)
	}
	missing := quote.PriceMinor - balance
	amount := max(missing, limits.Minimum())

	body := &strings.Builder{}
	if balance > 0 {
		body.WriteString(text(session.Locale, "stars.checkoutPartial", stars, html.EscapeString(review.Recipient),
			formatMoney(quote.PriceMinor, quote.Currency), formatMoney(balance, quote.Currency),
			formatMoney(missing, quote.Currency)))
	} else {
		body.WriteString(text(session.Locale, "stars.checkout", stars, html.EscapeString(review.Recipient),
			formatMoney(missing, quote.Currency)))
	}
	if amount > missing {
		body.WriteString("\n\n")
		body.WriteString(text(session.Locale, "stars.minimumTopUp", formatMoney(amount, quote.Currency)))
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(choices)+1)
	for _, choice := range choices {
		rows = append(rows, row(actionButton(text(session.Locale, "pay."+choice.Provider),
			"topup-pm:"+choice.Provider+":"+strconv.FormatInt(amount, 10))))
	}
	rows = append(rows, row(actionButton(text(session.Locale, "action.back"), "stars-qty:"+review.ID+":"+strconv.Itoa(stars))))
	return View{Text: body.String(), Keyboard: keyboard(rows...)}
}

// starsPayRemainder opens an order whose wallet part is spent now and whose
// remainder a payment settles, exactly as a shop pack is bought.
func (app *App) starsPayRemainder(
	ctx context.Context, session commerceContext, product ShopProduct, review ShopReview,
	stars int, isSelf bool, quote ShopQuote, provider string,
) View {
	order, err := app.commerce.StartStarsPurchase(ctx, session.Customer.ID, product, stars,
		review.Recipient, isSelf, quote, "goods:stars:"+review.ID+":"+strconv.Itoa(stars), false)
	switch {
	case errors.Is(err, commercepg.ErrMaintenance):
		return app.maintenanceScreen(ctx, session)
	case errors.Is(err, commercepg.ErrQuoteExpired), errors.Is(err, commercepg.ErrPriceChanged):
		return app.starsConfirmScreen(ctx, session, review, stars, text(session.Locale, "stars.priceChanged"))
	case err != nil:
		app.logger.Error("stars order creation failed", "error", err)
		return View{
			Text:     text(session.Locale, "error.order"),
			Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routeStars))),
		}
	}
	if order.ExternalMinor == 0 {
		return app.shopOrderScreen(ctx, session, order.ID)
	}
	if _, err = app.commerce.StartPayment(ctx, order, provider, session.TelegramID, product.Name); err != nil {
		app.logger.Error("stars payment intent failed", "error", err)
		return View{
			Text:     text(session.Locale, "error.payment"),
			Keyboard: keyboard(row(actionButton(text(session.Locale, "action.retry"), "order:"+order.ID))),
		}
	}
	refreshed, err := app.customers.Order(ctx, session.Customer.ID, order.ID, session.Locale)
	if err != nil {
		app.logger.Error("order lookup failed", "error", err)
		return app.errorView(session.Locale, routeOrders)
	}
	return orderStatusView(session.Locale, refreshed, nil)
}

// starsCatalog is the Stars flow's copy, merged into the bot catalogue.
var starsCatalog = map[string]phrase{
	"menu.stars": {ru: "⭐ Купить звёзды", en: "⭐ Buy Stars"},
	"stars.title": {ru: "⭐ <b>Звёзды Telegram</b>\n\nЗвёзды приходят на аккаунт получателя за пару минут.",
		en: "⭐ <b>Telegram Stars</b>\n\nStars reach the recipient's account within a couple of minutes."},
	"stars.unitPrice": {ru: "Цена: <b>%s</b> за звезду, от %d до %d шт.",
		en: "Price: <b>%s</b> per Star, %d to %d at a time."},
	"stars.bounds":          {ru: "От %d до %d звёзд за раз.", en: "%d to %d Stars at a time."},
	"stars.who":             {ru: "Кому отправить звёзды?", en: "Who should receive the Stars?"},
	"stars.toSelf":          {ru: "🙋 Себе (@%s)", en: "🙋 Myself (@%s)"},
	"stars.toOther":         {ru: "🎁 Другому", en: "🎁 Someone else"},
	"stars.myOrders":        {ru: "📜 Мои заказы", en: "📜 My orders"},
	"stars.viewOrder":       {ru: "🧾 Открыть заказ", en: "🧾 Open the order"},
	"stars.restart":         {ru: "⭐ Начать заново", en: "⭐ Start again"},
	"stars.expired":         {ru: "⌛ Выбор устарел, начните заново.", en: "⌛ That choice has expired; please start again."},
	"stars.askRecipient":    {ru: "🎁 Отправьте ник получателя в Telegram — например, <code>@durov</code>.\n\nЗвёзды придут на этот аккаунт, отменить отправку будет нельзя.", en: "🎁 Send the recipient's Telegram username — for example <code>@durov</code>.\n\nThe Stars go to that account and cannot be recalled."},
	"stars.badRecipient":    {ru: "❌ Не похоже на ник Telegram. Нужны латинские буквы, цифры и «_», от 5 до 32 символов.", en: "❌ That does not look like a Telegram username: 5 to 32 Latin letters, digits, and underscores."},
	"stars.askAmount":       {ru: "⭐ Получатель: <b>@%s</b>\n\nСколько звёзд отправить?", en: "⭐ Recipient: <b>@%s</b>\n\nHow many Stars?"},
	"stars.preset":          {ru: "%d ⭐ — %s", en: "%d ⭐ — %s"},
	"stars.customAmount":    {ru: "✏️ Другое количество", en: "✏️ Another amount"},
	"stars.askCustom":       {ru: "✏️ Напишите количество звёзд — от %d до %d.", en: "✏️ Type the number of Stars — %d to %d."},
	"stars.badAmount":       {ru: "❌ Нужно число от %d до %d.", en: "❌ That needs to be a number from %d to %d."},
	"stars.confirm":         {ru: "⭐ <b>Проверьте заказ</b>\n\nПолучатель: <b>@%s</b>\nКоличество: <b>%d ⭐</b>\nК оплате: <b>%s</b>\nНа балансе: %s", en: "⭐ <b>Check your order</b>\n\nRecipient: <b>@%s</b>\nAmount: <b>%d ⭐</b>\nTotal: <b>%s</b>\nWallet: %s"},
	"stars.pay":             {ru: "✅ Оплатить", en: "✅ Pay"},
	"stars.priceChanged":    {ru: "⚠️ Цена изменилась — проверьте заказ ещё раз.", en: "⚠️ The price has changed — please check the order again."},
	"stars.paid":            {ru: "✅ <b>Заказ оплачен</b>\n\n%d ⭐ для @%s — отправляем, обычно это пара минут. Мы напишем, когда звёзды придут.\n\nОстаток на балансе: %s", en: "✅ <b>Order paid</b>\n\n%d ⭐ for @%s — on their way, usually within a couple of minutes. We will message you when they arrive.\n\nWallet balance: %s"},
	"stars.checkout":        {ru: "⭐ <b>%d ⭐</b> для @%s\n\nК оплате: <b>%s</b>\n\nВыберите способ оплаты — звёзды придут автоматически через несколько секунд после оплаты.", en: "⭐ <b>%d ⭐</b> for @%s\n\nTo pay: <b>%s</b>\n\nChoose how to pay — the Stars are sent automatically a few seconds after the payment."},
	"stars.checkoutPartial": {ru: "⭐ <b>%d ⭐</b> для @%s\n\nСумма заказа: %s\nС баланса: %s\nДоплатить: <b>%s</b>\n\nВыберите способ оплаты — звёзды придут автоматически через несколько секунд после оплаты.", en: "⭐ <b>%d ⭐</b> for @%s\n\nOrder total: %s\nFrom your wallet: %s\nTo pay: <b>%s</b>\n\nChoose how to pay — the Stars are sent automatically a few seconds after the payment."},
	"stars.minimumTopUp":    {ru: "Минимальное пополнение — %s; остаток останется на балансе.", en: "The smallest top-up is %s; what is left stays in your wallet."},
	// Delivery notices.
	"stars.delivered":         {ru: "⭐ Готово! %d звёзд отправлены пользователю @%s.", en: "⭐ Done! %d Stars were sent to @%s."},
	"stars.goodsDelivered":    {ru: "✅ Готово: «%s» отправлено пользователю @%s.", en: "✅ Done: “%s” was sent to @%s."},
	"stars.refundedRecipient": {ru: "↩️ Не нашли пользователя @%s в Telegram. %s вернули на баланс.", en: "↩️ We could not find @%s on Telegram. %s is back in your wallet."},
	"stars.refunded":          {ru: "↩️ Не удалось отправить заказ для @%s. %s вернули на баланс.", en: "↩️ The order for @%s could not be delivered. %s is back in your wallet."},
	"stars.autoPurchased":     {ru: "✅ Баланс пополнен — заказ оплачен: %d ⭐ для @%s. Отправляем звёзды.", en: "✅ Your top-up landed and the order is paid: %d ⭐ for @%s. Sending the Stars now."},
	"stars.autoPriceChanged":  {ru: "⚠️ Цена звёзд изменилась, поэтому заказ %d ⭐ для @%s не оплачен автоматически. Откройте «Купить звёзды» и подтвердите заново.", en: "⚠️ The price of Stars changed, so the order of %d ⭐ for @%s was not paid automatically. Open “Buy Stars” and confirm again."},
	"stars.vpnTrial":          {ru: "🔐 Кстати, у нас есть VPN — попробуйте бесплатно.", en: "🔐 By the way, we also run a VPN — try it free."},
	"stars.vpnTrialButton":    {ru: "🎁 Попробовать VPN бесплатно", en: "🎁 Try the VPN free"},
	"stars.vpnBuy":            {ru: "🔐 Кстати, у нас есть VPN — подключается за минуту.", en: "🔐 By the way, we also run a VPN — it takes a minute to connect."},
	"stars.vpnBuyButton":      {ru: "🔐 Подключить VPN", en: "🔐 Get the VPN"},
}
