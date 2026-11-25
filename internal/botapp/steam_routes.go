package botapp

import (
	"context"
	"errors"
	"html"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/goods"
)

// The Steam flow: a Steam wallet top-up bought from the main menu, on top of
// the same order, wallet, and delivery pipeline every shop purchase uses.
//
// It runs login first (typed, or the one the customer topped up last time),
// then the amount in whole dollars (preset buttons or a typed number), then
// one confirmation that restates the login, the amount, the total, and the
// balance above the button that pays. A wallet that covers the total pays at
// once. One that does not either saves the purchase and offers to top up
// exactly the shortfall — for a price the operator set, the purchase then
// makes itself when the top-up settles — or opens an order whose remainder a
// payment settles. The entry appears only while a Steam product is on sale.

// routeSteam opens the Steam flow from the main menu.
const routeSteam = "steam"

// steamActions is the closed set of callback actions the Steam flow owns. It
// is folded into shopActions, which is what lets a tap reach
// handleSteamAction.
var steamActions = map[string]bool{
	"steam-login": true, "steam-last": true, "steam-amt": true, "steam-qty": true,
	"steam-custom": true, "steam-buy": true,
}

func init() {
	for action := range steamActions {
		shopActions[action] = true
	}
	for key, message := range steamCatalog {
		catalog[key] = message
	}
}

// steamProduct is the product the Steam flow sells: the first visible Steam
// top-up.
func (app *App) steamProduct(ctx context.Context, locale Locale) (ShopProduct, bool, error) {
	products, err := app.customers.ShopProducts(ctx, locale)
	if err != nil {
		return ShopProduct{}, false, err
	}
	for _, product := range products {
		if product.SteamTopUp() {
			return product, true, nil
		}
	}
	return ShopProduct{}, false, nil
}

// handleSteamAction serves the Steam flow's buttons. It reports false for an
// action it does not own.
func (app *App) handleSteamAction(ctx context.Context, session commerceContext, parts []string) (View, bool) {
	if !steamActions[parts[0]] {
		return View{}, false
	}
	argument := func(index int) string {
		if len(parts) > index {
			return parts[index]
		}
		return ""
	}
	switch parts[0] {
	case "steam-login":
		return app.steamAskLogin(ctx, session), true
	case "steam-last":
		return app.steamLastLogin(ctx, session), true
	}

	review, err := app.customers.ShopReviewFor(ctx, session.Customer.ID, argument(1))
	if err != nil {
		if !errors.Is(err, ErrShopReviewGone) {
			app.logError(ctx, "steam review lookup failed", "error", err)
		}
		return app.steamExpired(session), true
	}
	dollars, _ := strconv.Atoi(argument(2))
	switch parts[0] {
	case "steam-amt":
		return app.steamAmountScreen(ctx, session, review), true
	case "steam-custom":
		return app.steamAskAmount(ctx, session, review), true
	case "steam-qty":
		return app.steamConfirmScreen(ctx, session, review, dollars, ""), true
	default:
		return app.steamBuy(ctx, session, review, dollars), true
	}
}

// steamScreen is the entry: what the customer gets, the price, the bounds,
// and the login.
func (app *App) steamScreen(ctx context.Context, session commerceContext) View {
	product, found, err := app.steamProduct(ctx, session.Locale)
	if err != nil {
		app.logError(ctx, "steam product lookup failed", "error", err)
		return app.errorView(session.Locale, routeHome)
	}
	if !found {
		return View{
			Text:     text(session.Locale, "steam.unavailable"),
			Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.menu"), routeHome))),
		}
	}
	body := &strings.Builder{}
	body.WriteString(text(session.Locale, "steam.title"))
	body.WriteString("\n\n")
	if product.UnitMinor != nil {
		body.WriteString(text(session.Locale, "steam.unitPrice",
			formatMoney(*product.UnitMinor, product.Currency), product.MinStars, product.MaxStars))
	} else {
		body.WriteString(text(session.Locale, "steam.bounds", product.MinStars, product.MaxStars))
	}
	body.WriteString("\n\n")
	body.WriteString(text(session.Locale, "steam.who"))

	rows := make([][]models.InlineKeyboardButton, 0, 4)
	if last, ok := app.customers.LastSteamLogin(ctx, session.Customer.ID); ok {
		rows = append(rows, row(actionButton(text(session.Locale, "steam.useLast", last), "steam-last")))
	}
	rows = append(rows,
		row(actionButton(text(session.Locale, "steam.enterLogin"), "steam-login")),
		row(callbackButton(text(session.Locale, "stars.myOrders"), routeShopOrders)),
		row(callbackButton(text(session.Locale, "action.back"), routeHome)),
	)
	return View{Text: body.String(), Keyboard: keyboard(rows...)}
}

func (app *App) steamExpired(session commerceContext) View {
	return View{
		Text:     text(session.Locale, "stars.expired"),
		Keyboard: keyboard(row(callbackButton(text(session.Locale, "steam.restart"), routeSteam))),
	}
}

func (app *App) steamAskLogin(ctx context.Context, session commerceContext) View {
	product, found, err := app.steamProduct(ctx, session.Locale)
	if err != nil || !found {
		return app.steamScreen(ctx, session)
	}
	if err := app.customers.BeginSessionState(ctx, session.TelegramID, "goods_recipient",
		map[string]any{"productId": product.ID, "flow": "steam"}); err != nil {
		app.logError(ctx, "steam login prompt failed", "error", err)
		return app.errorView(session.Locale, routeSteam)
	}
	return View{
		Text:     text(session.Locale, "steam.askLogin"),
		Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routeSteam))),
	}
}

// steamLastLogin starts again with the login of the customer's last Steam
// top-up. It is read back from their own order rather than carried in the
// button, so callback data never holds it.
func (app *App) steamLastLogin(ctx context.Context, session commerceContext) View {
	last, ok := app.customers.LastSteamLogin(ctx, session.Customer.ID)
	if !ok {
		return app.steamAskLogin(ctx, session)
	}
	return app.steamWithLogin(ctx, session, last)
}

// SubmitSteamLogin handles the login typed in the Steam flow.
func (app *App) SubmitSteamLogin(ctx context.Context, session commerceContext, input string) View {
	login, err := goods.NormalizeSteamLogin(input)
	if err != nil {
		return View{
			Text: text(session.Locale, "steam.badLogin"),
			Keyboard: keyboard(
				row(actionButton(text(session.Locale, "steam.enterLogin"), "steam-login")),
				row(callbackButton(text(session.Locale, "action.back"), routeSteam)),
			),
		}
	}
	return app.steamWithLogin(ctx, session, login)
}

// steamWithLogin records the login as a review — the short-lived row every
// shop confirmation uses — so later buttons carry its identifier rather than
// the login, which keeps callback data under Telegram's 64 bytes.
func (app *App) steamWithLogin(ctx context.Context, session commerceContext, login string) View {
	product, found, err := app.steamProduct(ctx, session.Locale)
	if err != nil || !found {
		return app.steamScreen(ctx, session)
	}
	if errors.Is(app.commerce.CheckRecipient(ctx, product, login), goods.ErrRecipientUnknown) {
		return View{
			Text: text(session.Locale, "steam.loginUnknown", html.EscapeString(login)),
			Keyboard: keyboard(
				row(actionButton(text(session.Locale, "steam.enterLogin"), "steam-login")),
				row(callbackButton(text(session.Locale, "action.back"), routeSteam)),
			),
		}
	}
	reviewID, err := app.customers.OpenShopReview(ctx, session.Customer.ID, product.ID, login)
	if err != nil {
		app.logError(ctx, "steam review could not be recorded", "error", err)
		return app.errorView(session.Locale, routeSteam)
	}
	return app.steamAmountScreen(ctx, session, ShopReview{ID: reviewID, ProductID: product.ID, Recipient: login})
}

// steamReviewProduct reads the Steam product a review was opened for.
func (app *App) steamReviewProduct(ctx context.Context, session commerceContext, review ShopReview) (ShopProduct, bool) {
	product, found, err := app.customers.ShopProduct(ctx, session.Locale, review.ProductID)
	if err != nil || !found || !product.SteamTopUp() {
		return ShopProduct{}, false
	}
	return product, true
}

// steamAmountScreen offers the presets, each with its price, and a typed
// amount.
func (app *App) steamAmountScreen(ctx context.Context, session commerceContext, review ShopReview) View {
	product, ok := app.steamReviewProduct(ctx, session, review)
	if !ok {
		return app.steamExpired(session)
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(product.Presets)/2+3)
	current := make([]models.InlineKeyboardButton, 0, 2)
	for _, preset := range product.Presets {
		label := "$" + strconv.Itoa(preset)
		if quote, quoteErr := app.commerce.QuoteStars(ctx, product, 1, preset); quoteErr == nil {
			label = text(session.Locale, "steam.preset", preset, formatMoney(quote.PriceMinor, quote.Currency))
		}
		current = append(current, actionButton(label, "steam-qty:"+review.ID+":"+strconv.Itoa(preset)))
		if len(current) == 2 {
			rows = append(rows, current)
			current = make([]models.InlineKeyboardButton, 0, 2)
		}
	}
	if len(current) > 0 {
		rows = append(rows, current)
	}
	rows = append(rows,
		row(actionButton(text(session.Locale, "steam.customAmount"), "steam-custom:"+review.ID)),
		row(callbackButton(text(session.Locale, "action.back"), routeSteam)),
	)
	return View{
		Text:     text(session.Locale, "steam.askAmount", html.EscapeString(review.Recipient)),
		Keyboard: keyboard(rows...),
	}
}

func (app *App) steamAskAmount(ctx context.Context, session commerceContext, review ShopReview) View {
	product, ok := app.steamReviewProduct(ctx, session, review)
	if !ok {
		return app.steamExpired(session)
	}
	if err := app.customers.BeginSessionState(ctx, session.TelegramID, "goods_stars_amount",
		map[string]any{"reviewId": review.ID, "flow": "steam"}); err != nil {
		app.logError(ctx, "steam amount prompt failed", "error", err)
		return app.errorView(session.Locale, routeSteam)
	}
	return View{
		Text:     text(session.Locale, "steam.askCustom", product.MinStars, product.MaxStars),
		Keyboard: keyboard(row(actionButton(text(session.Locale, "action.back"), "steam-amt:"+review.ID))),
	}
}

// SubmitSteamAmount handles a typed amount: whole dollars, with an optional
// dollar sign.
func (app *App) SubmitSteamAmount(ctx context.Context, session commerceContext, reviewID, input string) View {
	review, err := app.customers.ShopReviewFor(ctx, session.Customer.ID, reviewID)
	if err != nil {
		return app.steamExpired(session)
	}
	product, ok := app.steamReviewProduct(ctx, session, review)
	if !ok {
		return app.steamExpired(session)
	}
	cleaned := strings.NewReplacer(" ", "", "$", "", " ", "").Replace(strings.TrimSpace(input))
	dollars, err := strconv.Atoi(cleaned)
	if err != nil || dollars < product.MinStars || dollars > product.MaxStars {
		return View{
			Text: text(session.Locale, "steam.badAmount", product.MinStars, product.MaxStars),
			Keyboard: keyboard(
				row(actionButton(text(session.Locale, "steam.customAmount"), "steam-custom:"+review.ID)),
				row(actionButton(text(session.Locale, "action.back"), "steam-amt:"+review.ID)),
			),
		}
	}
	return app.steamConfirmScreen(ctx, session, review, dollars, "")
}

// steamQuoteFailed is the screen for a top-up nobody can price right now.
func steamQuoteFailed(locale Locale) View {
	return View{
		Text:     text(locale, "steam.quoteFailed"),
		Keyboard: keyboard(row(callbackButton(text(locale, "action.back"), routeSteam))),
	}
}

// steamConfirmScreen restates everything above the button that pays.
func (app *App) steamConfirmScreen(
	ctx context.Context, session commerceContext, review ShopReview, dollars int, notice string,
) View {
	product, ok := app.steamReviewProduct(ctx, session, review)
	if !ok {
		return app.steamExpired(session)
	}
	if dollars < product.MinStars || dollars > product.MaxStars {
		return app.steamAmountScreen(ctx, session, review)
	}
	quote, err := app.commerce.QuoteStars(ctx, product, 1, dollars)
	if err != nil {
		app.logger.Warn("steam quote failed", "product", product.Code, "error", err)
		return steamQuoteFailed(session.Locale)
	}
	balance, err := app.commerce.AvailableBalance(ctx, session.Customer.ID, quote.Currency)
	if err != nil {
		app.logError(ctx, "wallet balance lookup failed", "error", err)
		return app.errorView(session.Locale, routeSteam)
	}
	body := &strings.Builder{}
	if notice != "" {
		body.WriteString(notice + "\n\n")
	}
	body.WriteString(text(session.Locale, "steam.confirm",
		html.EscapeString(review.Recipient), dollars,
		formatMoney(quote.PriceMinor, quote.Currency), formatMoney(max(balance, 0), quote.Currency)))
	body.WriteString("\n\n")
	body.WriteString(text(session.Locale, "steam.irreversible"))
	return View{
		Text: body.String(),
		Keyboard: keyboard(
			row(actionButton(text(session.Locale, "stars.pay"), "steam-buy:"+review.ID+":"+strconv.Itoa(dollars))),
			row(actionButton(text(session.Locale, "action.back"), "steam-amt:"+review.ID)),
		),
	}
}

// steamBuy pays from the wallet, or hands over to the checkout for what is
// missing.
func (app *App) steamBuy(ctx context.Context, session commerceContext, review ShopReview, dollars int) View {
	product, ok := app.steamReviewProduct(ctx, session, review)
	if !ok || dollars < product.MinStars || dollars > product.MaxStars {
		return app.steamExpired(session)
	}
	if gate := app.checkPurchaseChannels(ctx, session.Customer.ID, session.TelegramID); !gate.Allowed() {
		return channelGateView(session.Locale, gate, "steam-buy:"+review.ID+":"+strconv.Itoa(dollars))
	}
	quote, err := app.commerce.QuoteStars(ctx, product, 1, dollars)
	if err != nil {
		app.logger.Warn("steam quote failed", "product", product.Code, "error", err)
		return steamQuoteFailed(session.Locale)
	}
	balance, err := app.commerce.AvailableBalance(ctx, session.Customer.ID, quote.Currency)
	if err != nil {
		app.logError(ctx, "wallet balance lookup failed", "error", err)
		return app.errorView(session.Locale, routeSteam)
	}
	if balance < quote.PriceMinor {
		return app.steamCheckout(ctx, session, product, review, dollars, quote, max(balance, 0))
	}

	order, err := app.commerce.StartStarsPurchase(ctx, session.Customer.ID, product, dollars,
		review.Recipient, false, quote, steamOrderKey(review, dollars), true)
	switch {
	case errors.Is(err, commercepg.ErrRestricted):
		return restrictedView(session.Locale)
	case errors.Is(err, commercepg.ErrMaintenance):
		return app.maintenanceScreen(ctx, session)
	case errors.Is(err, commercepg.ErrPriceChanged), errors.Is(err, commercepg.ErrQuoteExpired):
		return app.steamConfirmScreen(ctx, session, review, dollars, text(session.Locale, "stars.priceChanged"))
	case errors.Is(err, commercepg.ErrWalletShort):
		// Another purchase spent the balance between the check and the order.
		return app.steamCheckout(ctx, session, product, review, dollars, quote, 0)
	case err != nil:
		app.logError(ctx, "steam order creation failed", "error", err)
		return View{
			Text:     text(session.Locale, "error.order"),
			Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routeSteam))),
		}
	}
	remaining, _ := app.commerce.AvailableBalance(ctx, session.Customer.ID, quote.Currency)
	return View{
		Text: text(session.Locale, "steam.paid", dollars, html.EscapeString(review.Recipient),
			formatMoney(max(remaining, 0), quote.Currency)),
		Keyboard: keyboard(
			row(actionButton(text(session.Locale, "stars.viewOrder"), "shop-order:"+order.ID)),
			row(callbackButton(text(session.Locale, "stars.myOrders"), routeShopOrders),
				callbackButton(text(session.Locale, "action.menu"), routeHome)),
		),
	}
}

// steamOrderKey names the review and the amount, so a double tap reaches the
// order the first tap made.
func steamOrderKey(review ShopReview, dollars int) string {
	return "goods:steam:" + review.ID + ":" + strconv.Itoa(dollars)
}

// steamCheckout is the purchase the wallet does not cover. For an operator's
// own price it is saved and the customer tops up exactly what is missing,
// after which it buys itself; for a price derived from PlayWallet's live
// cost, which must not be charged unattended, an order opens whose remainder
// a payment settles.
func (app *App) steamCheckout(
	ctx context.Context, session commerceContext, product ShopProduct, review ShopReview,
	dollars int, quote ShopQuote, balance int64,
) View {
	limits := app.commerce.TopUpLimits()
	choices := app.commerce.ExternalPaymentChoices(quote.Currency)
	if len(choices) == 0 {
		return View{
			Text:     text(session.Locale, "pay.none"),
			Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routeSteam))),
		}
	}
	if !product.OperatorPriced() || !limits.Enabled {
		return app.steamPayRemainder(ctx, session, product, review, dollars, quote, choices[0].Provider)
	}
	if err := app.commerce.SaveStarsForTopUp(ctx, session.Customer.ID, product, dollars,
		review.Recipient, false, quote); err != nil {
		app.logError(ctx, "steam cart save failed", "error", err)
		return app.errorView(session.Locale, routeSteam)
	}
	missing := quote.PriceMinor - balance
	amount := max(missing, limits.Minimum())

	login := html.EscapeString(review.Recipient)
	body := &strings.Builder{}
	if balance > 0 {
		body.WriteString(text(session.Locale, "steam.checkoutPartial", dollars, login,
			formatMoney(quote.PriceMinor, quote.Currency), formatMoney(balance, quote.Currency),
			formatMoney(missing, quote.Currency)))
	} else {
		body.WriteString(text(session.Locale, "steam.checkout", dollars, login,
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
	rows = append(rows, row(actionButton(text(session.Locale, "action.back"),
		"steam-qty:"+review.ID+":"+strconv.Itoa(dollars))))
	return View{Text: body.String(), Keyboard: keyboard(rows...)}
}

// steamPayRemainder opens an order whose wallet part is spent now and whose
// remainder a payment settles.
func (app *App) steamPayRemainder(
	ctx context.Context, session commerceContext, product ShopProduct, review ShopReview,
	dollars int, quote ShopQuote, provider string,
) View {
	order, err := app.commerce.StartStarsPurchase(ctx, session.Customer.ID, product, dollars,
		review.Recipient, false, quote, steamOrderKey(review, dollars), false)
	switch {
	case errors.Is(err, commercepg.ErrRestricted):
		return restrictedView(session.Locale)
	case errors.Is(err, commercepg.ErrMaintenance):
		return app.maintenanceScreen(ctx, session)
	case errors.Is(err, commercepg.ErrQuoteExpired), errors.Is(err, commercepg.ErrPriceChanged):
		return app.steamConfirmScreen(ctx, session, review, dollars, text(session.Locale, "stars.priceChanged"))
	case err != nil:
		app.logError(ctx, "steam order creation failed", "error", err)
		return View{
			Text:     text(session.Locale, "error.order"),
			Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routeSteam))),
		}
	}
	if order.ExternalMinor == 0 {
		return app.shopOrderScreen(ctx, session, order.ID)
	}
	if _, err = app.commerce.StartPayment(ctx, order, provider, session.TelegramID, product.Name); err != nil {
		app.logError(ctx, "steam payment intent failed", "error", err)
		return View{
			Text:     text(session.Locale, "error.payment"),
			Keyboard: keyboard(row(actionButton(text(session.Locale, "action.retry"), "order:"+order.ID))),
		}
	}
	refreshed, err := app.customers.Order(ctx, session.Customer.ID, order.ID, session.Locale)
	if err != nil {
		app.logError(ctx, "order lookup failed", "error", err)
		return app.errorView(session.Locale, routeOrders)
	}
	return orderStatusView(session.Locale, refreshed, nil)
}

// LastSteamLogin is the login of the customer's most recent Steam top-up, so
// a returning customer does not type it again. It is their own order's
// recipient, read with their identifier in the predicate.
func (store *PostgresStore) LastSteamLogin(ctx context.Context, customerID string) (string, bool) {
	var login string
	err := store.pool.QueryRow(ctx, `
		SELECT g.recipient_username FROM goods_orders g
		JOIN goods_products p ON p.id = g.product_id
		WHERE g.user_id = $1::uuid AND p.kind = 'steam_topup'
		ORDER BY g.created_at DESC LIMIT 1`, customerID).Scan(&login)
	if err != nil {
		return "", false
	}
	if _, invalid := goods.NormalizeSteamLogin(login); invalid != nil {
		return "", false
	}
	return login, true
}

// steamCatalog is the Steam flow's copy, merged into the bot catalogue.
var steamCatalog = map[string]phrase{
	"menu.steam": {ru: "🎮 Пополнить Steam", en: "🎮 Top up Steam"},
	"steam.title": {ru: "🎮 <b>Пополнение Steam</b>\n\nДеньги приходят на кошелёк Steam обычно за несколько минут. Сумма указывается в долларах, Steam зачисляет её в валюте аккаунта по своему курсу.",
		en: "🎮 <b>Steam top-up</b>\n\nThe money usually reaches the Steam wallet within a few minutes. The amount is in US dollars; Steam credits it in the account's own currency at its rate."},
	"steam.unitPrice":   {ru: "Цена: <b>%s</b> за $1, от $%d до $%d за раз.", en: "Price: <b>%s</b> per $1, $%d to $%d at a time."},
	"steam.bounds":      {ru: "От $%d до $%d за раз.", en: "$%d to $%d at a time."},
	"steam.who":         {ru: "Какой аккаунт пополнить?", en: "Which account should be topped up?"},
	"steam.useLast":     {ru: "🔁 Снова %s", en: "🔁 %s again"},
	"steam.enterLogin":  {ru: "✏️ Ввести логин Steam", en: "✏️ Enter a Steam login"},
	"steam.restart":     {ru: "🎮 Начать заново", en: "🎮 Start again"},
	"steam.unavailable": {ru: "🎮 Пополнение Steam сейчас недоступно.", en: "🎮 Steam top-ups are not available right now."},
	"steam.askLogin": {ru: "🎮 Отправьте <b>логин</b> Steam — имя, с которым вы входите в аккаунт, а не ник в профиле.\n\nПроверьте его внимательно: деньги придут на этот аккаунт, отменить пополнение будет нельзя.",
		en: "🎮 Send the Steam <b>login</b> — the account name you sign in with, not the profile name.\n\nCheck it carefully: the money goes to that account and the top-up cannot be undone."},
	"steam.badLogin": {ru: "❌ Не похоже на логин Steam: нужно от 3 до 64 латинских букв, цифр и «_».",
		en: "❌ That does not look like a Steam login: 3 to 64 Latin letters, digits, and underscores."},
	"steam.loginUnknown": {ru: "❌ Steam не нашёл аккаунт <code>%s</code> или его нельзя пополнить. Проверьте логин — это имя для входа, а не ник в профиле.",
		en: "❌ Steam has no account <code>%s</code>, or it cannot be topped up. Check the login — the name you sign in with, not the profile name."},
	"steam.askAmount":    {ru: "🎮 Логин Steam: <code>%s</code>\n\nНа сколько долларов пополнить?", en: "🎮 Steam login: <code>%s</code>\n\nHow many dollars should be added?"},
	"steam.preset":       {ru: "$%d — %s", en: "$%d — %s"},
	"steam.customAmount": {ru: "✏️ Другая сумма", en: "✏️ Another amount"},
	"steam.askCustom":    {ru: "✏️ Напишите сумму в долларах — целое число от %d до %d.", en: "✏️ Type the amount in dollars — a whole number from %d to %d."},
	"steam.badAmount":    {ru: "❌ Нужно целое число от %d до %d.", en: "❌ That needs to be a whole number from %d to %d."},
	"steam.quoteFailed":  {ru: "⚠️ Сейчас не удаётся рассчитать цену пополнения. Попробуйте позже.", en: "⚠️ The top-up cannot be priced right now. Please try again later."},
	"steam.confirm": {ru: "🎮 <b>Проверьте заказ</b>\n\nЛогин Steam: <code>%s</code>\nПополнение: <b>$%d</b>\nК оплате: <b>%s</b>\nНа балансе: %s",
		en: "🎮 <b>Check your order</b>\n\nSteam login: <code>%s</code>\nTop-up: <b>$%d</b>\nTotal: <b>%s</b>\nWallet: %s"},
	"steam.irreversible": {ru: "⚠️ Пополнение нельзя отменить, а деньги, отправленные на чужой аккаунт, — вернуть.",
		en: "⚠️ A top-up cannot be undone, and money sent to the wrong account cannot be recovered."},
	"steam.paid": {ru: "✅ <b>Заказ оплачен</b>\n\n$%d на Steam <code>%s</code> — пополняем, обычно это несколько минут. Мы напишем, когда деньги придут.\n\nОстаток на балансе: %s",
		en: "✅ <b>Order paid</b>\n\n$%d for Steam <code>%s</code> — on its way, usually within a few minutes. We will message you when it arrives.\n\nWallet balance: %s"},
	"steam.checkout": {ru: "🎮 <b>$%d</b> на Steam <code>%s</code>\n\nК оплате: <b>%s</b>\n\nВыберите способ оплаты — пополнение начнётся автоматически после оплаты.",
		en: "🎮 <b>$%d</b> for Steam <code>%s</code>\n\nTo pay: <b>%s</b>\n\nChoose how to pay — the top-up starts automatically once the payment lands."},
	"steam.checkoutPartial": {ru: "🎮 <b>$%d</b> на Steam <code>%s</code>\n\nСумма заказа: %s\nС баланса: %s\nДоплатить: <b>%s</b>\n\nВыберите способ оплаты — пополнение начнётся автоматически после оплаты.",
		en: "🎮 <b>$%d</b> for Steam <code>%s</code>\n\nOrder total: %s\nFrom your wallet: %s\nTo pay: <b>%s</b>\n\nChoose how to pay — the top-up starts automatically once the payment lands."},
	// Order history and delivery notices.
	"steam.loginLine":         {ru: "Логин Steam: %s", en: "Steam login: %s"},
	"steam.delivered":         {ru: "🎮 Готово! $%d зачислено на Steam <code>%s</code>.", en: "🎮 Done! $%d was added to Steam <code>%s</code>."},
	"steam.refundedRecipient": {ru: "↩️ Steam не нашёл аккаунт <code>%s</code>. %s вернули на баланс.", en: "↩️ Steam has no account <code>%s</code>. %s is back in your wallet."},
	"steam.refunded":          {ru: "↩️ Не удалось пополнить Steam <code>%s</code>. %s вернули на баланс.", en: "↩️ Steam <code>%s</code> could not be topped up. %s is back in your wallet."},
	"steam.autoPurchased":     {ru: "✅ Баланс пополнен — заказ оплачен: $%d на Steam <code>%s</code>. Пополняем.", en: "✅ Your top-up landed and the order is paid: $%d for Steam <code>%s</code>. Topping it up now."},
	"steam.autoPriceChanged": {ru: "⚠️ Цена изменилась, поэтому пополнение $%d на Steam <code>%s</code> не оплачено автоматически. Откройте «Пополнить Steam» и подтвердите заново.",
		en: "⚠️ The price changed, so the $%d top-up for Steam <code>%s</code> was not paid automatically. Open “Top up Steam” and confirm again."},
}
