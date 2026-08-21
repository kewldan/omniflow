package botapp

import (
	"context"
	"errors"
	"strings"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/goods"
)

// shopHistoryLimit bounds the history screen. A Telegram message has a length
// ceiling, and a customer looking for a recent purchase is not helped by two
// hundred rows.
const shopHistoryLimit = 10

// handleShopRoute serves the digital-goods screens.
//
// It returns false for anything it does not own, so the commerce router keeps
// its single dispatch point rather than growing a second one.
func (app *App) handleShopRoute(ctx context.Context, session commerceContext, route string) (View, bool) {
	switch route {
	case routeShop:
		return app.shopScreen(ctx, session), true
	case routeShopOrders:
		return app.shopOrdersScreen(ctx, session), true
	default:
		return View{}, false
	}
}

// shopActions is the closed set of callback actions the shop owns. It is
// folded into commerceActions, which is what lets a tap reach the handler
// below at all.
var shopActions = map[string]bool{
	"shop-item": true, "shop-self": true, "shop-other": true, "shop-buy": true,
	"shop-promo": true, "shop-unpromo": true, "shop-save": true, "shop-order": true,
}

// handleShopAction serves the shop's callback actions.
func (app *App) handleShopAction(ctx context.Context, session commerceContext, parts []string) (View, bool) {
	argument := ""
	if len(parts) > 1 {
		argument = parts[1]
	}
	switch parts[0] {
	case "shop-item":
		return app.shopItemScreen(ctx, session, argument), true
	case "shop-self":
		return app.shopBuyForSelf(ctx, session, argument), true
	case "shop-other":
		return app.shopAskRecipient(ctx, session, argument), true
	case "shop-buy":
		if len(parts) != 3 {
			return app.shopScreen(ctx, session), true
		}
		return app.shopPurchase(ctx, session, parts[1], parts[2]), true
	case "shop-promo":
		if len(parts) != 3 {
			return app.shopScreen(ctx, session), true
		}
		return app.shopAskPromo(ctx, session, parts[1], parts[2]), true
	case "shop-unpromo":
		if len(parts) != 3 {
			return app.shopScreen(ctx, session), true
		}
		return app.shopClearPromo(ctx, session, parts[1], parts[2]), true
	case "shop-save":
		if len(parts) != 3 {
			return app.shopScreen(ctx, session), true
		}
		return app.shopSaveForLater(ctx, session, parts[1], parts[2]), true
	case "shop-order":
		return app.shopOrderScreen(ctx, session, argument), true
	default:
		return View{}, false
	}
}

func (app *App) shopScreen(ctx context.Context, session commerceContext) View {
	products, err := app.customers.ShopProducts(ctx, session.Locale)
	if err != nil {
		app.logger.Error("shop catalog lookup failed", "error", err)
		return app.errorView(session.Locale, routeHome)
	}
	return shopCatalogView(session.Locale, products)
}

// shopItemScreen quotes a price the moment the customer opens the product.
//
// Quoting here rather than at confirmation is deliberate: the number on the
// screen is the number that will be charged, and its expiry is stated. A shop
// that quotes only at the end can show one price and take another.
func (app *App) shopItemScreen(ctx context.Context, session commerceContext, productID string) View {
	product, found, err := app.customers.ShopProduct(ctx, session.Locale, productID)
	if err != nil {
		app.logger.Error("shop product lookup failed", "error", err)
		return app.errorView(session.Locale, routeShop)
	}
	if !found {
		return app.shopScreen(ctx, session)
	}
	quote, err := app.commerce.QuoteGoods(ctx, product, 1)
	if err != nil {
		app.logger.Warn("shop quote failed", "product", product.Code, "error", err)
		return shopItemView(session.Locale, product, ShopQuote{}, true)
	}
	return shopItemView(session.Locale, product, quote, false)
}

// shopBuyForSelf skips the username prompt for a customer buying for
// themselves, but not the review step.
//
// The username is still shown and still confirmed, because the account the bot
// knows about and the account the customer wants the Stars on are not always
// the same one.
func (app *App) shopBuyForSelf(ctx context.Context, session commerceContext, productID string) View {
	if session.Username == "" {
		// Telegram only publishes a username when the customer has set one, and
		// delivery is addressed by username. Asking is the only option.
		return app.shopAskRecipient(ctx, session, productID)
	}
	return app.shopReview(ctx, session, productID, session.Username, true)
}

func (app *App) shopAskRecipient(ctx context.Context, session commerceContext, productID string) View {
	if err := app.customers.BeginSessionState(
		ctx, session.TelegramID, "goods_recipient", map[string]any{"productId": productID},
	); err != nil {
		app.logger.Error("shop recipient prompt failed", "error", err)
		return app.errorView(session.Locale, routeShop)
	}
	return shopRecipientPromptView(session.Locale)
}

// SubmitShopRecipient handles the username the customer typed.
func (app *App) SubmitShopRecipient(
	ctx context.Context, session commerceContext, productID, input string,
) View {
	recipient, err := goods.NormalizeRecipient(input)
	if err != nil {
		return View{
			Text:     text(session.Locale, "shop.recipientInvalid"),
			Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routeShop))),
		}
	}
	isSelf := strings.EqualFold(recipient, session.Username)
	return app.shopReview(ctx, session, productID, recipient, isSelf)
}

func (app *App) shopReview(
	ctx context.Context, session commerceContext, productID, recipient string, isSelf bool,
) View {
	product, found, err := app.customers.ShopProduct(ctx, session.Locale, productID)
	if err != nil || !found {
		return app.shopScreen(ctx, session)
	}
	quote, err := app.commerce.QuoteGoods(ctx, product, 1)
	if err != nil {
		app.logger.Warn("shop quote failed", "product", product.Code, "error", err)
		return shopItemView(session.Locale, product, ShopQuote{}, true)
	}
	return shopConfirmView(session.Locale, product, quote, recipient, isSelf,
		app.shopPromoState(ctx, session, product, quote))
}

// shopPromoState prices whatever code the customer has saved against the quote
// of the moment.
//
// It re-validates rather than trusting what was stored, because a code that was
// valid when it was typed may not be now: the promotion can have ended, and the
// provider rate can have moved and left no headroom. A code that no longer
// applies is reported as a rejection rather than silently dropped, so the
// customer learns what happened to the discount they were expecting.
func (app *App) shopPromoState(
	ctx context.Context, session commerceContext, product ShopProduct, quote ShopQuote,
) ShopPromoState {
	cart, found, err := app.commerce.SavedShopPurchase(ctx, session.Customer.ID)
	if err != nil || !found || cart.PromoCode == "" {
		return ShopPromoState{}
	}
	if cart.ProductID != product.ID {
		// The saved cart is for something else. Its code is not this purchase's
		// business.
		return ShopPromoState{}
	}
	discount, rejection := app.commerce.PreviewShopPromo(
		ctx, session.Customer.ID, product, quote, cart.PromoCode)
	return ShopPromoState{Code: cart.PromoCode, DiscountMinor: discount, Rejection: rejection}
}

// shopAskPromo prompts for a code, saving the purchase first so the code has
// something to attach to and so navigating away does not lose the selection.
func (app *App) shopAskPromo(
	ctx context.Context, session commerceContext, productID, recipient string,
) View {
	if _, err := app.saveShopCart(ctx, session, productID, recipient); err != nil {
		app.logger.Error("shop cart save failed", "error", err)
		return app.errorView(session.Locale, routeShop)
	}
	if err := app.customers.BeginSessionState(
		ctx, session.TelegramID, "goods_promo",
		map[string]any{"productId": productID, "recipient": recipient},
	); err != nil {
		app.logger.Error("shop promo prompt failed", "error", err)
		return app.errorView(session.Locale, routeShop)
	}
	return promoPromptView(session.Locale)
}

// SubmitShopPromo stores the code the customer typed and re-renders the review.
func (app *App) SubmitShopPromo(
	ctx context.Context, session commerceContext, productID, recipient, input string,
) View {
	if _, err := app.commerce.SetShopPromo(ctx, session.Customer.ID, input); err != nil {
		// An unusable code is not a failure of the purchase. The review screen
		// re-renders and says the code did not apply.
		app.logger.Info("shop promo rejected", "error", err)
	}
	isSelf := strings.EqualFold(recipient, session.Username)
	return app.shopReview(ctx, session, productID, recipient, isSelf)
}

func (app *App) shopClearPromo(
	ctx context.Context, session commerceContext, productID, recipient string,
) View {
	if _, err := app.commerce.SetShopPromo(ctx, session.Customer.ID, ""); err != nil {
		app.logger.Warn("shop promo removal failed", "error", err)
	}
	isSelf := strings.EqualFold(recipient, session.Username)
	return app.shopReview(ctx, session, productID, recipient, isSelf)
}

// shopSaveForLater keeps the purchase without charging for it.
//
// Unlike a saved plan, it never buys itself when a top-up lands. A shop price is
// a provider quote that expires, and charging one unattended would mean charging
// a number the customer last saw days ago.
func (app *App) shopSaveForLater(
	ctx context.Context, session commerceContext, productID, recipient string,
) View {
	product, err := app.saveShopCart(ctx, session, productID, recipient)
	if err != nil {
		app.logger.Error("shop cart save failed", "error", err)
		return app.errorView(session.Locale, routeShop)
	}
	return shopSavedView(session.Locale, product)
}

// saveShopCart stores the selection as the customer's open cart.
func (app *App) saveShopCart(
	ctx context.Context, session commerceContext, productID, recipient string,
) (ShopProduct, error) {
	product, found, err := app.customers.ShopProduct(ctx, session.Locale, productID)
	if err != nil || !found {
		return ShopProduct{}, errors.New("product is unavailable")
	}
	quote, err := app.commerce.QuoteGoods(ctx, product, 1)
	if err != nil {
		return ShopProduct{}, err
	}
	isSelf := strings.EqualFold(recipient, session.Username)
	if err := app.commerce.SaveShopPurchase(
		ctx, session.Customer.ID, product, recipient, isSelf, quote,
	); err != nil {
		return ShopProduct{}, err
	}
	return product, nil
}

// shopPurchase opens the order and starts payment.
//
// The wallet is applied first, exactly as it is for a plan, so a customer with
// a balance that covers the purchase never sees a payment provider at all.
func (app *App) shopPurchase(
	ctx context.Context, session commerceContext, productID, recipient string,
) View {
	product, found, err := app.customers.ShopProduct(ctx, session.Locale, productID)
	if err != nil || !found {
		return app.shopScreen(ctx, session)
	}
	// The same membership gate every purchase passes at the moment money moves.
	// The review screen is one tap away, so joining and confirming again
	// resumes here with the same recipient.
	if gate := app.checkPurchaseChannels(ctx, session.Customer.ID, session.TelegramID); !gate.Allowed() {
		return channelGateView(session.Locale, gate, "shop-buy:"+productID+":"+recipient)
	}
	quote, err := app.commerce.QuoteGoods(ctx, product, 1)
	if err != nil {
		return shopItemView(session.Locale, product, ShopQuote{}, true)
	}
	isSelf := strings.EqualFold(recipient, session.Username)

	// The promo code lives on the saved cart, which is where it went when the
	// customer typed it. Reading it here rather than threading it through the
	// callback data keeps the button under Telegram's payload limit and means a
	// customer who navigated away and came back still has their discount.
	promoCode := ""
	var savedCartID string
	if saved, found, savedErr := app.commerce.SavedShopPurchase(
		ctx, session.Customer.ID,
	); savedErr == nil && found && saved.ProductID == product.ID {
		promoCode, savedCartID = saved.PromoCode, saved.CartID
	}

	order, err := app.commerce.StartShopPurchase(
		ctx, session.Customer.ID, product, 1, recipient, isSelf, quote, false, promoCode,
	)
	switch {
	case errors.Is(err, commercepg.ErrMaintenance):
		return app.maintenanceScreen(ctx, session)
	case errors.Is(err, commercepg.ErrQuoteExpired):
		// The rate moved while the customer was deciding. Re-quoting is the
		// honest response; charging the stale number is not.
		return app.shopItemScreen(ctx, session, productID)
	case errors.Is(err, commercepg.ErrPromoUnknown),
		errors.Is(err, commercepg.ErrPromoIneligible),
		errors.Is(err, commercepg.ErrPromoExhausted),
		errors.Is(err, commercepg.ErrPromoBelowCost),
		errors.Is(err, commercepg.ErrPromoInvalid):
		// The code stopped applying between the review screen and the button.
		// Re-rendering the review says so and leaves the purchase available
		// without the discount, rather than failing the whole thing.
		return app.shopReview(ctx, session, productID, recipient, isSelf)
	case err != nil:
		app.logger.Error("shop order creation failed", "error", err)
		return View{
			Text:     text(session.Locale, "error.order"),
			Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routeShop))),
		}
	}

	// The cart did its job. Closing it here rather than at delivery means a
	// customer who buys and comes back does not find the same thing still
	// saved, which reads as a second pending purchase.
	if savedCartID != "" {
		if err = app.commerce.CloseSavedShopPurchase(ctx, savedCartID, order.ID); err != nil {
			app.logger.Warn("shop cart close failed", "error", err)
		}
	}

	if order.ExternalMinor == 0 {
		// Fully covered by the wallet: already paid, already queued for
		// delivery, and no provider was involved.
		return app.shopOrderScreen(ctx, session, order.ID)
	}
	choices := app.commerce.ExternalPaymentChoices(order.Currency)
	if len(choices) == 0 {
		return View{
			Text:     text(session.Locale, "pay.none"),
			Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routeShop))),
		}
	}
	if _, err = app.commerce.StartPayment(
		ctx, order, choices[0].Provider, session.TelegramID, product.Name,
	); err != nil {
		app.logger.Error("shop payment intent failed", "error", err)
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

func (app *App) shopOrdersScreen(ctx context.Context, session commerceContext) View {
	orders, err := app.customers.ShopOrders(ctx, session.Customer.ID, session.Locale, shopHistoryLimit)
	if err != nil {
		app.logger.Error("shop history lookup failed", "error", err)
		return app.errorView(session.Locale, routeShop)
	}
	return shopOrdersView(session.Locale, orders)
}

func (app *App) shopOrderScreen(ctx context.Context, session commerceContext, orderID string) View {
	order, found, err := app.customers.ShopOrderFor(ctx, session.Customer.ID, orderID, session.Locale)
	if err != nil {
		app.logger.Error("shop order lookup failed", "error", err)
		return app.errorView(session.Locale, routeShopOrders)
	}
	if !found {
		return app.shopOrdersScreen(ctx, session)
	}
	return shopOrderView(session.Locale, order)
}
