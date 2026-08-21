package botapp

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/payments"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// loadExpansionView renders the v0.5 routes: wallet top-up, the saved cart, and
// the subscription switcher.
func (app *App) loadExpansionView(ctx context.Context, session commerceContext, route string) (View, bool) {
	switch route {
	case routeTopUp:
		return app.topUpScreen(ctx, session), true
	case routeCart:
		return app.cartScreen(ctx, session), true
	case routeSubscriptions:
		return app.subscriptionsScreen(ctx, session), true
	default:
		return View{}, false
	}
}

func (app *App) topUpScreen(ctx context.Context, session commerceContext) View {
	currency := app.settings.Currency
	balance, err := app.customers.WalletBalance(ctx, session.Customer.ID, currency)
	if err != nil {
		app.logger.Error("wallet balance lookup failed", "error", err)
		return app.errorView(session.Locale, routeWallet)
	}
	credited, err := app.commerce.TopUpAllowance(ctx, session.Customer.ID, currency)
	if err != nil {
		app.logger.Error("top-up allowance lookup failed", "error", err)
		return app.errorView(session.Locale, routeWallet)
	}
	return topUpView(session.Locale, app.commerce.TopUpLimits(), currency, balance, credited)
}

// beginTopUpAmount opens the free-entry flow. The amount arrives as an ordinary
// message and is validated against the same limits as a preset.
func (app *App) beginTopUpAmount(ctx context.Context, session commerceContext) View {
	if err := app.customers.BeginSessionState(ctx, session.TelegramID, "topup_amount", nil); err != nil {
		app.logger.Error("top-up prompt failed", "error", err)
		return app.errorView(session.Locale, routeTopUp)
	}
	limits := app.commerce.TopUpLimits()
	return View{
		Text:     text(session.Locale, "topup.prompt", formatMoney(limits.Minimum(), app.settings.Currency), formatMoney(limits.MaximumMinor, app.settings.Currency)),
		Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.cancel"), routeTopUp))),
	}
}

// topUpMethodScreen validates the requested amount before offering methods, so
// a rejected amount is explained here rather than after a payment attempt.
func (app *App) topUpMethodScreen(ctx context.Context, session commerceContext, amountMinor int64) View {
	currency := app.settings.Currency
	credited, err := app.commerce.TopUpAllowance(ctx, session.Customer.ID, currency)
	if err != nil {
		app.logger.Error("top-up allowance lookup failed", "error", err)
		return app.errorView(session.Locale, routeTopUp)
	}
	if reason, validateErr := app.commerce.TopUpLimits().Validate(amountMinor, credited); validateErr != nil {
		return View{
			Text:     text(session.Locale, "topup.rejected", text(session.Locale, "topup."+reason)),
			Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routeTopUp))),
		}
	}
	return topUpMethodView(session.Locale, amountMinor, currency, app.commerce.ExternalPaymentChoices(currency))
}

// startTopUp creates the top-up order and its payment. Both steps are keyed on
// one freshly minted idempotency key, so a redelivered callback cannot create a
// second order.
func (app *App) startTopUp(ctx context.Context, session commerceContext, provider string, amountMinor int64) View {
	choices := app.commerce.ExternalPaymentChoices(app.settings.Currency)
	offered := false
	for _, choice := range choices {
		if choice.Provider == provider {
			offered = true
			break
		}
	}
	// Callback data is customer-controlled and cannot be trusted to name a
	// compatible adapter.
	if !offered {
		return View{Text: text(session.Locale, "pay.none"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.wallet"), routeWallet)))}
	}
	order, err := app.commerce.StartTopUp(ctx, session.Customer.ID, app.settings.Currency, amountMinor, provider, session.TelegramID)
	if reason := TopUpRejectionReason(err); reason != "" {
		return View{Text: text(session.Locale, "topup.rejected", text(session.Locale, "topup."+reason)), Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routeTopUp)))}
	}
	if errors.Is(err, commercepg.ErrMaintenance) {
		return app.maintenanceScreen(ctx, session)
	}
	if err != nil {
		app.logger.Error("top-up could not be started", "provider", provider, "error", err)
		return View{Text: text(session.Locale, "error.payment"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routeTopUp)))}
	}
	summary, err := app.customers.Order(ctx, session.Customer.ID, order.ID, session.Locale)
	if err != nil {
		app.logger.Error("top-up order lookup failed", "error", err)
		return app.errorView(session.Locale, routeTopUp)
	}
	return orderStatusView(session.Locale, summary, nil)
}

// completeTopUpAmount handles the free-entry message. The amount is parsed as a
// major-unit value the customer typed and converted to minor units.
func (app *App) completeTopUpAmount(ctx context.Context, session commerceContext, raw string) View {
	amount, err := parseAmountMinor(raw, app.settings.Currency)
	if err != nil {
		return View{
			Text:     text(session.Locale, "topup.badAmount"),
			Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routeTopUp))),
		}
	}
	if cancelErr := app.customers.CancelSession(ctx, session.TelegramID); cancelErr != nil {
		app.logger.Warn("session cleanup failed", "error", cancelErr)
	}
	return app.topUpMethodScreen(ctx, session, amount)
}

// parseAmountMinor converts a customer-typed major-unit amount into minor units
// using the currency's own exponent. Telegram Stars have no minor units.
func parseAmountMinor(raw, currency string) (int64, error) {
	normalized := strings.ReplaceAll(strings.TrimSpace(raw), ",", ".")
	normalized = strings.ReplaceAll(normalized, " ", "")
	if normalized == "" {
		return 0, errors.New("empty amount")
	}
	exponent := currencyExponent(currency)
	whole, fraction, _ := strings.Cut(normalized, ".")
	value, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || value < 0 {
		return 0, errors.New("invalid amount")
	}
	scale := int64(1)
	for range exponent {
		scale *= 10
	}
	if value > (1<<62)/max(scale, 1) {
		return 0, errors.New("amount is too large")
	}
	minor := value * scale
	if fraction == "" || exponent == 0 {
		return minor, nil
	}
	if len(fraction) > exponent {
		fraction = fraction[:exponent]
	}
	for len(fraction) < exponent {
		fraction += "0"
	}
	part, err := strconv.ParseInt(fraction, 10, 64)
	if err != nil || part < 0 {
		return 0, errors.New("invalid amount")
	}
	return minor + part, nil
}

// cartScreen renders the saved cart with a freshly re-validated price.
func (app *App) cartScreen(ctx context.Context, session commerceContext) View {
	cart, quote, found, err := app.commerce.Cart(ctx, session.Customer.ID)
	if errors.Is(err, commercepg.ErrPlanUnavailable) || errors.Is(err, commercepg.ErrAddonUnavailable) {
		return View{Text: text(session.Locale, "cart.stale"), Keyboard: keyboard(row(actionButton(text(session.Locale, "cart.clear"), "cart-clear")), row(callbackButton(text(session.Locale, "menu.plans"), routePlans)))}
	}
	if err != nil {
		app.logger.Error("cart lookup failed", "error", err)
		return app.errorView(session.Locale, routeCart)
	}
	if !found {
		return cartEmptyView(session.Locale)
	}
	planName := ""
	if plan, planErr := app.customers.Plan(ctx, uuidText(cart.PlanVersionID), session.Locale, cart.Currency); planErr == nil {
		planName = plan.Name
	}
	return cartView(session.Locale, cart, quote, planName, time.Now().UTC())
}

// saveCart turns the open checkout into a saved cart. It is what makes an
// insufficient balance a pause rather than a dead end.
func (app *App) saveCart(ctx context.Context, session commerceContext) View {
	checkout, found, err := app.customers.Checkout(ctx, session.Customer.ID)
	if err != nil || !found {
		return View{Text: text(session.Locale, "checkout.expired"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.plans"), routePlans)))}
	}
	addons, err := app.customers.CheckoutAddons(ctx, checkout.ID)
	if err != nil {
		app.logger.Error("checkout add-on lookup failed", "error", err)
		return app.errorView(session.Locale, routeCart)
	}
	if _, err = app.commerce.SaveCart(ctx, checkout, addons, true); err != nil {
		app.logger.Error("cart could not be saved", "error", err)
		return app.errorView(session.Locale, routeCart)
	}
	return app.cartScreen(ctx, session)
}

func (app *App) setCartAutoPurchase(ctx context.Context, session commerceContext, enabled bool) View {
	if err := app.commerce.SetCartAutoPurchase(ctx, session.Customer.ID, enabled); err != nil {
		app.logger.Error("cart auto-purchase update failed", "error", err)
		return app.errorView(session.Locale, routeCart)
	}
	return app.cartScreen(ctx, session)
}

func (app *App) clearCart(ctx context.Context, session commerceContext) View {
	if err := app.commerce.DiscardCart(ctx, session.Customer.ID); err != nil {
		app.logger.Error("cart could not be cleared", "error", err)
		return app.errorView(session.Locale, routeCart)
	}
	return cartEmptyView(session.Locale)
}

// purchaseCart charges a saved cart on demand. It shares the automatic path's
// idempotency key, so tapping it while the sweep is running cannot double-charge.
func (app *App) purchaseCart(ctx context.Context, session commerceContext) View {
	purchase, err := app.commerce.PurchaseCart(ctx, session.Customer.ID)
	if err != nil {
		app.logger.Error("cart purchase failed", "error", err)
		return app.errorView(session.Locale, routeCart)
	}
	if !purchase.Purchased {
		reason := purchase.Reason
		if reason == "" {
			reason = commerce.CartInsufficientBalance
		}
		return View{
			Text:     text(session.Locale, "cart.rejected", text(session.Locale, "cart."+reason)),
			Keyboard: keyboard(row(callbackButton(text(session.Locale, "cart.topUp"), routeTopUp)), row(callbackButton(text(session.Locale, "action.back"), routeCart))),
		}
	}
	summary, err := app.customers.Order(ctx, session.Customer.ID, purchase.OrderID, session.Locale)
	if err != nil {
		app.logger.Error("cart order lookup failed", "error", err)
		return app.errorView(session.Locale, routeOrders)
	}
	return orderStatusView(session.Locale, summary, nil)
}

// subscriptionsScreen is the switcher. With concurrent subscriptions disabled it
// opens the single subscription directly, so the one-screen experience is
// unchanged for installations that never turn the feature on.
func (app *App) subscriptionsScreen(ctx context.Context, session commerceContext) View {
	subscriptions, err := app.customers.Subscriptions(ctx, session.Customer.ID, session.Locale)
	if err != nil {
		app.logger.Error("subscription lookup failed", "error", err)
		return app.errorView(session.Locale, routeSubscriptions)
	}
	policy := app.commerce.SubscriptionPolicy()
	if len(subscriptions) == 1 && !policy.MultiEnabled {
		return app.subscriptionDetailScreen(ctx, session, subscriptions[0].ID)
	}
	canAdd := policy.MultiEnabled && len(subscriptions) < policy.EffectiveMax()
	return subscriptionsView(session.Locale, subscriptions, canAdd, time.Now().UTC())
}

func (app *App) subscriptionDetailScreen(ctx context.Context, session commerceContext, subscriptionID string) View {
	subscription, err := app.customers.Subscription(ctx, session.Customer.ID, subscriptionID, session.Locale)
	if errors.Is(err, ErrSubscriptionNotFound) {
		return View{Text: text(session.Locale, "subs.gone"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "subs.back"), routeSubscriptions)))}
	}
	if err != nil {
		app.logger.Error("subscription lookup failed", "error", err)
		return app.errorView(session.Locale, routeSubscriptions)
	}
	hasAddons := false
	if subscription.Found {
		if addons, addonErr := app.addonsFor(ctx, session, subscription); addonErr == nil {
			hasAddons = len(addons) > 0
		}
	}
	return subscriptionDetailView(session.Locale, subscription, time.Now().UTC(), hasAddons)
}

// addonsFor lists the add-ons offered for a subscription's current plan — that
// subscription's, not whichever of the customer's subscriptions ends last.
func (app *App) addonsFor(ctx context.Context, session commerceContext, subscription SubscriptionSummary) ([]Addon, error) {
	entitlement, err := app.customers.EntitlementForSubscription(ctx, session.Customer.ID, subscription.ID, session.Locale, app.settings.Currency)
	if err != nil || !entitlement.Found {
		return nil, err
	}
	return app.customers.PlanAddons(ctx, entitlement.PlanVersionID, session.Locale, app.settings.Currency)
}

func (app *App) addonListScreen(ctx context.Context, session commerceContext, subscriptionID string) View {
	subscription, err := app.customers.Subscription(ctx, session.Customer.ID, subscriptionID, session.Locale)
	if err != nil {
		return View{Text: text(session.Locale, "subs.gone"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "subs.back"), routeSubscriptions)))}
	}
	addons, err := app.addonsFor(ctx, session, subscription)
	if err != nil {
		app.logger.Error("add-on lookup failed", "error", err)
		return app.errorView(session.Locale, routeSubscriptions)
	}
	return addonListView(session.Locale, subscription, addons)
}

// buyAddon purchases one add-on for a subscription. The wallet settles it when
// it can; otherwise the customer is handed the ordinary payment screen.
func (app *App) buyAddon(ctx context.Context, session commerceContext, addonVersionID, subscriptionID string) View {
	provider := ""
	if choices := app.commerce.ExternalPaymentChoices(app.settings.Currency); len(choices) > 0 {
		provider = choices[0].Provider
	}
	summary, err := app.commerce.BuyAddon(ctx, session.Customer.ID, subscriptionID, app.settings.Currency, addonVersionID, 1, provider, session.TelegramID)
	switch {
	case errors.Is(err, commercepg.ErrMaintenance):
		return app.maintenanceScreen(ctx, session)
	case errors.Is(err, commercepg.ErrNoActiveSubscription):
		return View{Text: text(session.Locale, "addon.noSubscription"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.plans"), routePlans)))}
	case errors.Is(err, commercepg.ErrAddonUnavailable):
		return View{Text: text(session.Locale, "addon.unavailable"), Keyboard: keyboard(row(actionButton(text(session.Locale, "action.back"), "addons:"+subscriptionID)))}
	case err != nil:
		app.logger.Error("add-on purchase failed", "error", err)
		return View{Text: text(session.Locale, "error.order"), Keyboard: keyboard(row(actionButton(text(session.Locale, "action.back"), "addons:"+subscriptionID)))}
	}
	return orderStatusView(session.Locale, summary, nil)
}

// beginSubscriptionRename opens the rename flow for one subscription.
func (app *App) beginSubscriptionRename(ctx context.Context, session commerceContext, subscriptionID string) View {
	if _, err := app.customers.Subscription(ctx, session.Customer.ID, subscriptionID, session.Locale); err != nil {
		return View{Text: text(session.Locale, "subs.gone"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "subs.back"), routeSubscriptions)))}
	}
	if err := app.customers.BeginSessionState(ctx, session.TelegramID, "subscription_label", map[string]any{"subscriptionId": subscriptionID}); err != nil {
		app.logger.Error("rename prompt failed", "error", err)
		return app.errorView(session.Locale, routeSubscriptions)
	}
	return View{Text: text(session.Locale, "subs.renamePrompt"), Keyboard: keyboard(row(actionButton(text(session.Locale, "action.cancel"), "sub:"+subscriptionID)))}
}

func (app *App) completeSubscriptionRename(ctx context.Context, session commerceContext, subscriptionID, label string) View {
	err := app.customers.RenameSubscription(ctx, session.Customer.ID, subscriptionID, label)
	if errors.Is(err, ErrSubscriptionNotFound) {
		return View{Text: text(session.Locale, "subs.gone"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "subs.back"), routeSubscriptions)))}
	}
	if err != nil {
		return View{Text: text(session.Locale, "subs.renameRejected"), Keyboard: keyboard(row(actionButton(text(session.Locale, "action.back"), "sub:"+subscriptionID)))}
	}
	if cleanupErr := app.customers.CancelSession(ctx, session.TelegramID); cleanupErr != nil {
		app.logger.Warn("session cleanup failed", "error", cleanupErr)
	}
	return app.subscriptionDetailScreen(ctx, session, subscriptionID)
}

// subscriptionRemnawaveView renders a per-subscription Remnawave screen, so
// device management and link rotation always act on the right VPN user.
func (app *App) subscriptionRemnawaveView(ctx context.Context, session commerceContext, subscriptionID, screen string) View {
	subscription, err := app.customers.Subscription(ctx, session.Customer.ID, subscriptionID, session.Locale)
	if err != nil || !subscription.Provisioned() {
		return View{Text: text(session.Locale, "subs.notProvisioned"), Keyboard: keyboard(row(actionButton(text(session.Locale, "action.back"), "sub:"+subscriptionID)))}
	}
	switch screen {
	case "connect":
		result, lookupErr := app.remnawave.Subscription(ctx, subscription.RemnawaveID)
		if lookupErr != nil && !errors.Is(lookupErr, remnawave.ErrNotFound) {
			app.logger.Error("Remnawave subscription lookup failed", "error", lookupErr)
			return app.errorView(session.Locale, routeSubscriptions)
		}
		return app.connectPlatformsScreen(ctx, session.Locale, result)
	case "devices":
		user, userErr := app.remnawave.User(ctx, subscription.RemnawaveID)
		if userErr != nil {
			app.logger.Error("Remnawave user lookup failed", "error", userErr)
			return app.errorView(session.Locale, routeSubscriptions)
		}
		devices, deviceErr := app.remnawave.Devices(ctx, subscription.RemnawaveID)
		if deviceErr != nil {
			app.logger.Error("Remnawave device lookup failed", "error", deviceErr)
			return app.errorView(session.Locale, routeSubscriptions)
		}
		return devicesView(session.Locale, devices, user.HWIDDeviceLimit)
	default:
		return app.subscriptionDetailScreen(ctx, session, subscriptionID)
	}
}

// rotateSubscriptionLink revokes and reissues one subscription's access link.
func (app *App) rotateSubscriptionLink(ctx context.Context, session commerceContext, subscriptionID string) View {
	subscription, err := app.customers.Subscription(ctx, session.Customer.ID, subscriptionID, session.Locale)
	if err != nil || !subscription.Provisioned() {
		return View{Text: text(session.Locale, "subs.notProvisioned"), Keyboard: keyboard(row(actionButton(text(session.Locale, "action.back"), "sub:"+subscriptionID)))}
	}
	if err = app.remnawave.RevokeSubscription(ctx, subscription.RemnawaveID); err != nil {
		app.logger.Error("subscription rotation failed", "error", err)
		return app.errorView(session.Locale, routeSubscriptions)
	}
	return app.subscriptionRemnawaveView(ctx, session, subscriptionID, "connect")
}

// blockedByMaintenance reports whether the purchase surface is paused and, if
// so, returns the notice to show instead. A lookup failure never blocks a
// customer: maintenance is an operator signal, not a fail-closed control.
func (app *App) blockedByMaintenance(ctx context.Context, session commerceContext) (View, bool) {
	state, err := app.customers.Maintenance(ctx)
	if err != nil {
		app.logger.Warn("maintenance state lookup failed", "error", err)
		return View{}, false
	}
	if !state.Blocks(commerce.ActionPurchase) {
		return View{}, false
	}
	return maintenanceView(session.Locale, state), true
}

// maintenanceScreen explains a paused installation to a customer.
func (app *App) maintenanceScreen(ctx context.Context, session commerceContext) View {
	state, err := app.customers.Maintenance(ctx)
	if err != nil {
		app.logger.Warn("maintenance state lookup failed", "error", err)
		state = commerce.Maintenance{Active: true, Source: commerce.MaintenanceManual}
	}
	return maintenanceView(session.Locale, state)
}

// expansionActions is the closed set of callback actions the v0.5 surfaces own.
var expansionActions = map[string]bool{
	"topup": true, "topup-custom": true, "topup-pm": true, "topup-history": true,
	"cart-save": true, "cart-auto": true, "cart-clear": true, "cart-buy": true,
	"sub": true, "sub-new": true, "sub-rename": true, "sub-connect": true,
	"sub-devices": true, "sub-renew": true, "sub-revoke-confirm": true, "sub-revoke": true,
	"squad": true, "addons": true, "addon-buy": true, "maintenance": true,
	"methods": true, "pm-change": true, "checkout-addons": true, "addon-toggle": true,
	"ops": true, "ops-backup": true, "ops-restore-confirm": true, "ops-restore": true,
}

// handleExpansionAction runs the callback actions the v0.5 surfaces own.
func (app *App) handleExpansionAction(ctx context.Context, session commerceContext, parts []string) (View, bool) {
	if !expansionActions[parts[0]] {
		return View{}, false
	}
	argument := ""
	if len(parts) > 1 {
		argument = parts[1]
	}
	switch parts[0] {
	case "topup":
		amount, err := strconv.ParseInt(argument, 10, 64)
		if err != nil {
			return app.topUpScreen(ctx, session), true
		}
		return app.topUpMethodScreen(ctx, session, amount), true
	case "topup-custom":
		return app.beginTopUpAmount(ctx, session), true
	case "topup-pm":
		if len(parts) != 3 {
			return app.topUpScreen(ctx, session), true
		}
		amount, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return app.topUpScreen(ctx, session), true
		}
		return app.startTopUp(ctx, session, parts[1], amount), true
	case "topup-history":
		history, err := app.commerce.TopUpHistory(ctx, session.Customer.ID, 10)
		if err != nil {
			app.logger.Error("top-up history lookup failed", "error", err)
			return app.errorView(session.Locale, routeTopUp), true
		}
		return topUpHistoryView(session.Locale, history), true
	case "cart-save":
		return app.saveCart(ctx, session), true
	case "cart-auto":
		return app.setCartAutoPurchase(ctx, session, argument == "on"), true
	case "cart-clear":
		return app.clearCart(ctx, session), true
	case "cart-buy":
		return app.purchaseCart(ctx, session), true
	case "sub":
		return app.subscriptionDetailScreen(ctx, session, argument), true
	case "sub-new":
		return app.plansScreenForNewSubscription(ctx, session), true
	case "sub-rename":
		return app.beginSubscriptionRename(ctx, session, argument), true
	case "sub-connect":
		return app.subscriptionRemnawaveView(ctx, session, argument, "connect"), true
	case "sub-devices":
		return app.subscriptionRemnawaveView(ctx, session, argument, "devices"), true
	case "sub-renew":
		return app.renewSubscription(ctx, session, argument), true
	case "sub-revoke-confirm":
		return View{
			Text: text(session.Locale, "subs.rotateConfirm"),
			Keyboard: keyboard(
				row(actionButton(text(session.Locale, "action.confirm"), "sub-revoke:"+argument)),
				row(actionButton(text(session.Locale, "action.cancel"), "sub:"+argument)),
			),
		}, true
	case "sub-revoke":
		return app.rotateSubscriptionLink(ctx, session, argument), true
	case "squad":
		return app.toggleSquad(ctx, session, argument), true
	case "methods":
		return app.paymentMethodsForCheckout(ctx, session, ""), true
	case "pm-change":
		return app.paymentMethodsForCheckout(ctx, session, "checkout"), true
	case "checkout-addons":
		return app.checkoutAddonScreen(ctx, session), true
	case "addon-toggle":
		return app.toggleCheckoutAddon(ctx, session, argument), true
	case "addons":
		return app.addonListScreen(ctx, session, argument), true
	case "addon-buy":
		if len(parts) != 3 {
			return app.errorView(session.Locale, routeSubscriptions), true
		}
		return app.buyAddon(ctx, session, parts[1], parts[2]), true
	case "maintenance":
		return app.maintenanceScreen(ctx, session), true
	case "ops", "ops-backup", "ops-restore-confirm", "ops-restore":
		return app.handleOperatorAction(ctx, session.TelegramID, parts)
	default:
		return View{}, false
	}
}

// plansScreenForNewSubscription opens the catalog with the intent to add a
// subscription rather than change the current one.
func (app *App) plansScreenForNewSubscription(ctx context.Context, session commerceContext) View {
	subscriptions, err := app.customers.Subscriptions(ctx, session.Customer.ID, session.Locale)
	if err != nil {
		app.logger.Error("subscription lookup failed", "error", err)
		return app.errorView(session.Locale, routeSubscriptions)
	}
	policy := app.commerce.SubscriptionPolicy()
	if err = policy.AllowAdditional(len(subscriptions), 0, nil); err != nil {
		reason := commerce.SubscriptionRejectionReason(err)
		return View{
			Text:     text(session.Locale, "subs.rejected", text(session.Locale, "subs."+reason)),
			Keyboard: keyboard(row(callbackButton(text(session.Locale, "subs.back"), routeSubscriptions))),
		}
	}
	// The catalogue is opened with the new-subscription intent, which every
	// plan button then carries to the checkout: this is the one path that must
	// open a slot rather than revive an expired subscription of the same plan.
	return app.plansScreen(ctx, session, true)
}

// renewSubscription starts a renewal checkout aimed at one named subscription.
//
// The plan renewed is that subscription's own: a customer holding two
// subscriptions on different plans who renews the second must not be handed
// the first one's plan, which is what reading the account-wide entitlement —
// the latest by end date across every subscription — used to do.
func (app *App) renewSubscription(ctx context.Context, session commerceContext, subscriptionID string) View {
	subscription, err := app.customers.Subscription(ctx, session.Customer.ID, subscriptionID, session.Locale)
	if err != nil || !subscription.Found {
		return View{Text: text(session.Locale, "subs.gone"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "subs.back"), routeSubscriptions)))}
	}
	entitlement, err := app.customers.EntitlementForSubscription(ctx, session.Customer.ID, subscriptionID, session.Locale, app.settings.Currency)
	if err != nil || !entitlement.Found {
		return app.plansScreen(ctx, session, false)
	}
	return app.paymentMethodScreen(ctx, session, entitlement.PlanVersionID, "extension", subscriptionID, false)
}

// paymentMethodsForCheckout offers the payment methods for the open checkout.
// The squad configurator hands off here, so a configured plan reaches the same
// method list a plain plan does, and the summary's "Change method" lands here
// too: the checkout — its promo code, add-ons, wallet toggle, squads, and the
// subscription it targets — is left exactly as it is, and only the method
// changes. Reopening the checkout to change the method would throw all of that
// away and silently retarget a renewal at the primary subscription.
//
// `back` names the action the Back button returns to: the plan page when the
// method is being chosen for the first time, the summary when it is being
// changed.
func (app *App) paymentMethodsForCheckout(ctx context.Context, session commerceContext, back string) View {
	checkout, found, err := app.customers.Checkout(ctx, session.Customer.ID)
	if err != nil || !found {
		return View{Text: text(session.Locale, "checkout.expired"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.plans"), routePlans)))}
	}
	plan, err := app.customers.Plan(ctx, checkout.PlanVersionID, session.Locale, checkout.Currency)
	if err != nil {
		return View{Text: text(session.Locale, "plan.gone"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routePlans)))}
	}
	// A required selection must actually have been made before the customer can
	// reach payment, so the plan's own policy is re-checked here.
	if policy, policyErr := app.customers.PlanSquads(ctx, checkout.PlanVersionID, session.Locale); policyErr == nil && policy.Configurable() {
		if _, resolveErr := squadPolicyFor(policy).ResolveSquads(checkout.SelectedSquadIDs); resolveErr != nil {
			return squadConfiguratorView(session.Locale, policy, checkout.SelectedSquadIDs)
		}
	}
	choices, err := app.commerce.PaymentChoices(ctx, checkout.PlanVersionID)
	if err != nil {
		app.logger.Error("payment method lookup failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	return paymentMethodView(session.Locale, plan, choices, back)
}

// squadPolicyFor projects the bot's configurator model onto the domain policy
// that validates a selection.
func squadPolicyFor(policy PlanSquadPolicy) commerce.SquadPolicy {
	offered := make([]string, 0, len(policy.Offered))
	for _, squad := range policy.Offered {
		offered = append(offered, squad.SquadID)
	}
	return commerce.SquadPolicy{Selection: policy.Selection, Offered: offered, Minimum: policy.Minimum, Maximum: policy.Maximum}
}

// checkoutAddonScreen offers add-ons for the plan being bought. They are charged
// on the same order, so the customer confirms and pays once.
func (app *App) checkoutAddonScreen(ctx context.Context, session commerceContext) View {
	checkout, found, err := app.customers.Checkout(ctx, session.Customer.ID)
	if err != nil || !found {
		return View{Text: text(session.Locale, "checkout.expired"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.plans"), routePlans)))}
	}
	addons, err := app.customers.PlanAddons(ctx, checkout.PlanVersionID, session.Locale, checkout.Currency)
	if err != nil {
		app.logger.Error("add-on lookup failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	selected, err := app.customers.CheckoutAddons(ctx, checkout.ID)
	if err != nil {
		app.logger.Error("checkout add-on lookup failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	return checkoutAddonView(session.Locale, addons, selected)
}

// toggleCheckoutAddon adds or removes one add-on from the open checkout.
func (app *App) toggleCheckoutAddon(ctx context.Context, session commerceContext, addonVersionID string) View {
	checkout, found, err := app.customers.Checkout(ctx, session.Customer.ID)
	if err != nil || !found {
		return View{Text: text(session.Locale, "checkout.expired"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.plans"), routePlans)))}
	}
	addons, err := app.customers.PlanAddons(ctx, checkout.PlanVersionID, session.Locale, checkout.Currency)
	if err != nil {
		app.logger.Error("add-on lookup failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	// Callback data is customer-controlled: only an add-on the plan actually
	// offers may be attached.
	offered := false
	for _, addon := range addons {
		if addon.AddonVersionID == addonVersionID {
			offered = true
			break
		}
	}
	if !offered {
		return View{Text: text(session.Locale, "addon.unavailable"), Keyboard: keyboard(row(actionButton(text(session.Locale, "action.back"), "checkout")))}
	}
	if err = app.customers.ToggleCheckoutAddon(ctx, checkout.ID, addonVersionID); err != nil {
		app.logger.Error("checkout add-on toggle failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	return app.checkoutAddonScreen(ctx, session)
}

// toggleSquad flips one optional squad in the open checkout.
func (app *App) toggleSquad(ctx context.Context, session commerceContext, squadID string) View {
	checkout, found, err := app.customers.Checkout(ctx, session.Customer.ID)
	if err != nil || !found {
		return View{Text: text(session.Locale, "checkout.expired"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.plans"), routePlans)))}
	}
	policy, err := app.customers.PlanSquads(ctx, checkout.PlanVersionID, session.Locale)
	if err != nil {
		app.logger.Error("squad policy lookup failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	// Callback data is customer-controlled: only a squad the plan actually
	// offers may be selected.
	offered := false
	for _, squad := range policy.Offered {
		if squad.SquadID == squadID {
			offered = true
			break
		}
	}
	if !offered {
		return squadConfiguratorView(session.Locale, policy, checkout.SelectedSquadIDs)
	}
	updated, err := app.customers.ToggleCheckoutSquad(ctx, checkout, squadID)
	if err != nil {
		app.logger.Error("squad selection failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	return squadConfiguratorView(session.Locale, policy, updated.SelectedSquadIDs)
}

// currencyExponent is how many minor units make one major unit. Telegram Stars
// have none, and an unknown currency is treated as having none so a typed amount
// is never silently scaled by a wrong factor.
func currencyExponent(currency string) int {
	exponent, err := payments.CurrencyExponent(strings.ToUpper(currency))
	if err != nil {
		return 0
	}
	return exponent
}
