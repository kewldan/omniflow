package botapp

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// MenuState carries the badges the main menu shows without a second round trip.
type MenuState struct {
	CommerceEnabled bool
	// ShopEnabled is true when the operator has published at least one visible
	// digital-goods product.
	ShopEnabled bool
	// OfferCount is how many targeted offers are waiting. The menu shows the
	// entry only when there is something in it, so a customer who has never been
	// targeted never sees an empty screen.
	OfferCount    int
	UnreadSupport int
	UnreadNews    int
	// MultiSubscription swaps the single subscription entry for the switcher.
	MultiSubscription bool
	// TopUpEnabled and HasCart keep the menu honest: a button appears only when
	// the screen behind it can actually be used.
	TopUpEnabled bool
	HasCart      bool
	// RecoverSubscriptionID names the subscription whose access has lapsed and
	// can be restored with a renewal. The home screen promises "restore access
	// in one tap" whenever a subscription has expired; this is the tap.
	RecoverSubscriptionID string
}

// commerceContext is everything a commerce screen needs about the caller. It is
// resolved once per update so a screen never re-derives identity mid-render.
type commerceContext struct {
	Customer   Customer
	Locale     Locale
	TelegramID int64
	// Username is the customer's Telegram handle as it arrived on this update.
	// Omniflow does not store it: a handle can be changed or dropped at any
	// time, so keeping a copy would mean holding a stale identifier for people
	// who have moved on. It is used only to pre-fill a recipient the customer
	// then confirms.
	Username string
}

// commerceEnabled reports whether the operator configured the commerce surface.
func (app *App) commerceEnabled() bool { return app.commerce != nil && app.customers != nil }

func (app *App) commerceContext(ctx context.Context, telegramID int64, telegramLocale string, username ...string) (commerceContext, error) {
	customer, err := app.customers.EnsureCustomer(ctx, telegramID, telegramLocale)
	if err != nil {
		return commerceContext{}, err
	}
	locale := localeFrom(customer.Locale)
	if preferences, prefErr := app.customers.CustomerPreferences(ctx, customer.ID); prefErr == nil && preferences.Locale != "auto" {
		locale = localeFrom(preferences.Locale)
	} else if prefErr == nil && telegramLocale != "" {
		locale = localeFrom(telegramLocale)
	}
	handle := ""
	if len(username) > 0 {
		handle = username[0]
	}
	return commerceContext{
		Customer: customer, Locale: locale, TelegramID: telegramID, Username: handle,
	}, nil
}

// menuState collects the unread badges for the main menu.
func (app *App) menuState(ctx context.Context, customerID string, locale Locale) MenuState {
	state := MenuState{
		CommerceEnabled:   true,
		MultiSubscription: app.commerce.SubscriptionPolicy().MultiEnabled,
		TopUpEnabled:      app.commerce.TopUpLimits().Enabled,
	}
	if unread, err := app.customers.UnreadSupportCount(ctx, customerID); err == nil {
		state.UnreadSupport = unread
	}
	if unread, err := app.customers.UnreadNewsCount(ctx, customerID, locale); err == nil {
		state.UnreadNews = unread
	}
	if _, _, found, err := app.commerce.Cart(ctx, customerID); err == nil {
		state.HasCart = found
	}
	if products, err := app.customers.ShopProducts(ctx, locale); err == nil {
		state.ShopEnabled = len(products) > 0
	}
	if offers, err := app.customers.ActiveOffers(ctx, customerID, locale, 1); err == nil {
		state.OfferCount = len(offers)
	}
	state.RecoverSubscriptionID = app.recoverableSubscription(ctx, customerID, locale)
	return state
}

// recoverableSubscription finds the subscription a "restore access" button
// should renew: the customer's latest entitlement when it has expired or is in
// its grace period. It is empty when access is simply healthy or when there is
// nothing to renew, and a lookup failure leaves the button out rather than the
// screen broken.
func (app *App) recoverableSubscription(ctx context.Context, customerID string, locale Locale) string {
	entitlement, err := app.customers.Entitlement(ctx, customerID, locale, app.settings.Currency)
	if err != nil || !entitlement.Found || entitlement.SubscriptionID == "" {
		return ""
	}
	phase := commerce.EvaluatePhase(time.Now().UTC(), commerce.Subscription{Status: entitlement.Status, EndsAt: entitlement.EndsAt, GracePeriod: entitlement.GracePeriod})
	if phase == commerce.PhaseExpired || phase == commerce.PhaseGrace {
		return entitlement.SubscriptionID
	}
	return ""
}

// loadCommerceView renders the routes the commerce surface owns. The second
// result reports whether this route was handled here.
func (app *App) loadCommerceView(ctx context.Context, session commerceContext, route string) (View, bool) {
	if !app.commerceEnabled() {
		return View{}, false
	}
	if view, handled := app.loadExpansionView(ctx, session, route); handled {
		return view, true
	}
	switch route {
	case routePlans:
		return app.plansScreen(ctx, session, false), true
	case routeOrders:
		return app.ordersScreen(ctx, session), true
	case routeWallet:
		return app.walletScreen(ctx, session), true
	case routeNews:
		return app.newsScreen(ctx, session), true
	case routeSupport:
		return app.supportScreen(ctx, session), true
	case routeShop, routeShopOrders:
		if view, handled := app.handleShopRoute(ctx, session, route); handled {
			return view, true
		}
		return View{}, false
	case routeGifts:
		return app.giftsScreen(ctx, session), true
	case routeOffers:
		return app.offersScreen(ctx, session), true
	case routeAutoRenew:
		return app.autoRenewScreen(ctx, session), true
	case routeMethods:
		return app.savedMethodsScreen(ctx, session), true
	default:
		return View{}, false
	}
}

// newSubscriptionFlag is the trailing callback segment that carries "the
// customer asked for an additional subscription" from the catalogue through
// the plan page to the checkout. Without it the intent behind "Add a
// subscription" was lost at the first tap and a purchase could revive an
// expired subscription the customer meant to keep beside a new one.
const newSubscriptionFlag = "new"

func (app *App) plansScreen(ctx context.Context, session commerceContext, forNew bool) View {
	// Maintenance stops the purchase surface at its entry point rather than at
	// the payment step, so a customer is told before they choose anything.
	if view, blocked := app.blockedByMaintenance(ctx, session); blocked {
		return view
	}
	plans, err := app.customers.Plans(ctx, session.Locale, app.settings.Currency)
	if err != nil {
		app.logger.Error("plan catalog lookup failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	return plansView(session.Locale, plans, app.settings.Currency, forNew)
}

func (app *App) planScreen(ctx context.Context, session commerceContext, planVersionID string, forNew bool) View {
	plan, err := app.customers.Plan(ctx, planVersionID, session.Locale, app.settings.Currency)
	if errors.Is(err, ErrPlanUnavailable) {
		return View{Text: text(session.Locale, "plan.gone"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routePlans)))}
	}
	if err != nil {
		app.logger.Error("plan lookup failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	entitlement, err := app.customers.Entitlement(ctx, session.Customer.ID, session.Locale, app.settings.Currency)
	if err != nil {
		app.logger.Error("entitlement lookup failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	trialReason := ""
	if plan.Kind == "trial" {
		request, trialErr := app.customers.TrialContext(ctx, session.Customer.ID)
		if trialErr != nil {
			app.logger.Error("trial eligibility lookup failed", "error", trialErr)
			return app.errorView(session.Locale, routePlans)
		}
		request.PlanKind, request.Rule = plan.Kind, plan.TrialRule
		request.MinimumAccountAge = app.settings.MinimumTrialAccountAge
		if reason, evalErr := commerce.EvaluateTrial(request); evalErr != nil {
			trialReason = reason
		}
	}
	return planView(session.Locale, plan, entitlement, app.termsURL, trialReason, forNew)
}

// paymentMethodScreen starts a checkout and asks which configured adapter should
// settle it. Choosing the method is what fixes the order currency.
func (app *App) paymentMethodScreen(ctx context.Context, session commerceContext, planVersionID, operation, subscriptionID string, forceNew bool) View {
	plan, err := app.customers.Plan(ctx, planVersionID, session.Locale, app.settings.Currency)
	if errors.Is(err, ErrPlanUnavailable) {
		return View{Text: text(session.Locale, "plan.gone"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routePlans)))}
	}
	if err != nil {
		app.logger.Error("plan lookup failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	normalized, err := commerce.NormalizeOperation(operation)
	if err != nil || !commerce.AllowedOperation(normalized, plan.UpgradePolicy, plan.DowngradePolicy) {
		return View{Text: text(session.Locale, "error.forbidden"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routePlans)))}
	}
	// A change to an existing subscription must name it. A plain purchase in a
	// single-subscription installation targets the one the customer already has,
	// so buying again renews it instead of silently opening a second one.
	target, normalized, err := app.checkoutTarget(ctx, session, plan, normalized, subscriptionID, forceNew)
	if err != nil {
		app.logger.Error("subscription target lookup failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	checkout, err := app.customers.OpenCheckout(ctx, session.Customer.ID, planVersionID, normalized, app.settings.Currency, target, nil)
	if err != nil {
		app.logger.Error("checkout could not be opened", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	// A plan that lets the customer choose squads asks before it asks for money.
	if policy, policyErr := app.customers.PlanSquads(ctx, planVersionID, session.Locale); policyErr == nil && policy.Configurable() {
		return squadConfiguratorView(session.Locale, policy, checkout.SelectedSquadIDs)
	}
	choices, err := app.commerce.PaymentChoices(ctx, planVersionID)
	if err != nil {
		app.logger.Error("payment method lookup failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	return paymentMethodView(session.Locale, plan, choices, "plan:"+plan.PlanVersionID)
}

// checkoutTarget resolves which subscription a checkout changes, and with it
// the operation that change really is.
//
// An explicit identifier always wins. Otherwise a change targets the primary
// subscription. A purchase opens a new subscription only where concurrency is
// enabled — and even there, a purchase of a plan the customer already holds on
// an expired subscription revives that subscription with an extension instead
// of opening a slot beside it: every expiry would otherwise mint a new
// subscription, a new Remnawave user, and a new link, until the account hit its
// concurrency limit and could buy nothing at all. `forceNew` is the "Add a
// subscription" path, the one place a new slot is what was asked for.
func (app *App) checkoutTarget(ctx context.Context, session commerceContext, plan Plan, operation, subscriptionID string, forceNew bool) (string, string, error) {
	if subscriptionID != "" {
		if _, err := app.customers.Subscription(ctx, session.Customer.ID, subscriptionID, session.Locale); err != nil {
			return "", operation, err
		}
		return subscriptionID, operation, nil
	}
	if commerce.TargetsNewSubscription(operation) && app.commerce.SubscriptionPolicy().MultiEnabled {
		if forceNew {
			return "", operation, nil
		}
		subscriptions, err := app.customers.Subscriptions(ctx, session.Customer.ID, session.Locale)
		if err != nil {
			return "", operation, err
		}
		if revive := expiredSubscriptionOfPlan(subscriptions, plan.PlanID, time.Now().UTC()); revive != "" {
			return revive, "extension", nil
		}
		return "", operation, nil
	}
	primary, err := app.customers.PrimarySubscription(ctx, session.Customer.ID, session.Locale)
	if errors.Is(err, ErrSubscriptionNotFound) {
		return "", operation, nil
	}
	if err != nil {
		return "", operation, err
	}
	return primary.ID, operation, nil
}

// expiredSubscriptionOfPlan finds the subscription a purchase of `planID`
// should revive: one that holds that plan and has run out. The lowest slot wins
// when several qualify, which is the order the list arrives in.
func expiredSubscriptionOfPlan(subscriptions []SubscriptionSummary, planID string, now time.Time) string {
	if planID == "" {
		return ""
	}
	for _, subscription := range subscriptions {
		if !subscription.Found || subscription.PlanID != planID {
			continue
		}
		if subscription.Phase(now, 0, 0) == commerce.PhaseExpired {
			return subscription.ID
		}
	}
	return ""
}

// checkoutScreen renders the confirmation summary for the open checkout.
func (app *App) checkoutScreen(ctx context.Context, session commerceContext) View {
	checkout, found, err := app.customers.Checkout(ctx, session.Customer.ID)
	if err != nil {
		app.logger.Error("checkout lookup failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	if !found {
		return View{Text: text(session.Locale, "checkout.expired"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.plans"), routePlans)))}
	}
	plan, err := app.customers.Plan(ctx, checkout.PlanVersionID, session.Locale, checkout.Currency)
	if errors.Is(err, ErrPlanUnavailable) {
		return View{Text: text(session.Locale, "plan.gone"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routePlans)))}
	}
	if err != nil {
		app.logger.Error("plan lookup failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	quote, err := app.commerce.Quote(ctx, checkout)
	if err != nil {
		app.logger.Error("checkout quote failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	addons, err := app.customers.PlanAddons(ctx, checkout.PlanVersionID, session.Locale, checkout.Currency)
	if err != nil {
		app.logger.Warn("add-on lookup failed", "error", err)
	}
	return checkoutView(session.Locale, plan, checkout, quote, len(addons) > 0)
}

// confirmCheckout creates the order and immediately starts the payment. Both
// steps are idempotent, so a duplicate confirmation lands on the same order.
func (app *App) confirmCheckout(ctx context.Context, session commerceContext) View {
	checkout, found, err := app.customers.Checkout(ctx, session.Customer.ID)
	if err != nil {
		app.logger.Error("checkout lookup failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	if !found {
		return View{Text: text(session.Locale, "checkout.expired"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.plans"), routePlans)))}
	}
	plan, err := app.customers.Plan(ctx, checkout.PlanVersionID, session.Locale, checkout.Currency)
	if err != nil {
		return View{Text: text(session.Locale, "plan.gone"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routePlans)))}
	}
	// Membership is verified here rather than when the plan was chosen: a
	// customer can leave a channel between browsing and paying, and this is the
	// last moment before money moves. The checkout is left intact, so joining
	// and pressing the button again resumes exactly where they were.
	if gate := app.checkPurchaseChannels(ctx, session.Customer.ID, session.TelegramID); !gate.Allowed() {
		return channelGateView(session.Locale, gate, "")
	}
	orderID, err := app.commerce.Confirm(ctx, checkout, plan, session.Customer)
	switch {
	case errors.Is(err, commercepg.ErrMaintenance):
		return app.maintenanceScreen(ctx, session)
	case errors.Is(err, commerce.ErrSubscriptionRejected):
		return View{
			Text:     text(session.Locale, "subs.rejected", text(session.Locale, "subs."+commerce.SubscriptionRejectionReason(err))),
			Keyboard: keyboard(row(callbackButton(text(session.Locale, "subs.back"), routeSubscriptions))),
		}
	case errors.Is(err, commerce.ErrSquadSelection):
		return View{
			Text:     text(session.Locale, "squad.title") + "\n\n" + text(session.Locale, "error.forbidden"),
			Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.plans"), routePlans))),
		}
	case errors.Is(err, commerce.ErrTrialNotEligible):
		return View{Text: text(session.Locale, "trial.rejected", text(session.Locale, "trial."+trialReasonOf(err))), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.plans"), routePlans)))}
	case errors.Is(err, commercepg.ErrTrialAlreadyClaimed):
		return View{Text: text(session.Locale, "trial.rejected", text(session.Locale, "trial.trial_already_used")), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.plans"), routePlans)))}
	case errors.Is(err, commercepg.ErrOperationForbidden):
		return View{Text: text(session.Locale, "error.forbidden"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.plans"), routePlans)))}
	case err != nil:
		app.logger.Error("order creation failed", "error", err)
		return View{Text: text(session.Locale, "error.order"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.plans"), routePlans)))}
	}
	order, err := app.customers.Order(ctx, session.Customer.ID, orderID, session.Locale)
	if err != nil {
		app.logger.Error("order lookup failed", "error", err)
		return app.errorView(session.Locale, routeOrders)
	}
	// A fully wallet-funded order is already settled and needs no provider.
	if order.ExternalMinor == 0 {
		return orderStatusView(session.Locale, order, nil)
	}
	description := plan.Name + " · " + formatDuration(session.Locale, plan.Duration)
	if _, err = app.commerce.StartPayment(ctx, order, checkout.Provider, session.TelegramID, description); err != nil {
		app.logger.Error("payment intent creation failed", "provider", checkout.Provider, "error", err)
		// The order exists and holds its wallet reservation; only the provider
		// payment is missing. "Try again" starts that payment rather than
		// reopening the order screen, which would have nothing to pay with.
		return paymentStartFailedView(session.Locale, order.ID)
	}
	order, err = app.customers.Order(ctx, session.Customer.ID, orderID, session.Locale)
	if err != nil {
		app.logger.Error("order lookup failed", "error", err)
		return app.errorView(session.Locale, routeOrders)
	}
	return orderStatusView(session.Locale, order, nil)
}

// trialReasonOf recovers the machine reason from a wrapped trial rejection.
func trialReasonOf(err error) string {
	message := err.Error()
	if index := strings.LastIndex(message, ": "); index >= 0 {
		return message[index+2:]
	}
	return "not_a_trial_plan"
}

func (app *App) orderScreen(ctx context.Context, session commerceContext, orderID string) View {
	order, err := app.customers.Order(ctx, session.Customer.ID, orderID, session.Locale)
	if errors.Is(err, ErrOrderNotFound) {
		return View{Text: text(session.Locale, "error.notFound"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.orders"), routeOrders)))}
	}
	if err != nil {
		app.logger.Error("order lookup failed", "error", err)
		return app.errorView(session.Locale, routeOrders)
	}
	// Refreshing a pending order re-reads the provider so a customer who came
	// back before the webhook still sees the settled state.
	app.commerce.Refresh(ctx, order)
	if order, err = app.customers.Order(ctx, session.Customer.ID, orderID, session.Locale); err != nil {
		app.logger.Error("order lookup failed", "error", err)
		return app.errorView(session.Locale, routeOrders)
	}
	refunds, err := app.customers.Refunds(ctx, session.Customer.ID, orderID)
	if err != nil {
		app.logger.Warn("refund lookup failed", "error", err)
	}
	return orderView(session.Locale, order, refunds, app.orderExtras(ctx, session, order))
}

// orderExtras reads what the order screen says about a gift or a shop
// purchase beyond the order row. A failed lookup degrades the copy rather than
// the screen.
func (app *App) orderExtras(ctx context.Context, session commerceContext, order OrderSummary) orderExtras {
	extras := orderExtras{}
	switch order.Operation {
	case "gift":
		gift, found, err := app.customers.GiftByOrder(ctx, session.Customer.ID, order.ID)
		if err != nil {
			app.logger.Warn("gift lookup failed", "error", err)
		} else if found {
			extras.Gift = &gift
		}
	case "goods":
		shop, found, err := app.customers.ShopOrderFor(ctx, session.Customer.ID, order.ID, session.Locale)
		if err != nil {
			app.logger.Warn("shop order lookup failed", "error", err)
		} else if found {
			extras.Shop = &shop
		}
	}
	return extras
}

// payOrder starts — or restarts — the payment of an order that has none in
// flight: a checkout whose payment provider refused the first attempt, or one
// whose intent failed. The method the checkout recorded is resumed when it is
// still offered and has not already failed for this order; otherwise the
// customer picks one from the same list the checkout offered.
//
// Nothing here creates an order. The order already exists and holds its wallet
// reservation; all this does is attach a provider payment to it, which is
// idempotent per order and provider.
func (app *App) payOrder(ctx context.Context, session commerceContext, orderID string, pick bool) View {
	order, err := app.customers.Order(ctx, session.Customer.ID, orderID, session.Locale)
	if errors.Is(err, ErrOrderNotFound) {
		return View{Text: text(session.Locale, "error.notFound"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.orders"), routeOrders)))}
	}
	if err != nil {
		app.logger.Error("order lookup failed", "error", err)
		return app.errorView(session.Locale, routeOrders)
	}
	if !orderAwaitsPayment(order) {
		return app.orderScreen(ctx, session, orderID)
	}
	choices := app.orderPaymentChoices(order)
	provider, err := app.customers.OrderCheckoutProvider(ctx, session.Customer.ID, orderID)
	if err != nil {
		app.logger.Warn("checkout provider lookup failed", "error", err)
	}
	if !pick && provider != "" && paymentChoiceOffered(choices, provider, order.Currency) {
		return app.startOrderPayment(ctx, session, order, provider)
	}
	return orderPaymentMethodView(session.Locale, order, choices)
}

// selectOrderProvider starts the payment of an order with the method the
// customer just picked. The method must be one the screen offered: callback
// data is customer-controlled and cannot be trusted to name a compatible
// adapter, and the order's currency is fixed, so the choice is validated
// against it rather than read from the callback.
func (app *App) selectOrderProvider(ctx context.Context, session commerceContext, orderID, provider string) View {
	order, err := app.customers.Order(ctx, session.Customer.ID, orderID, session.Locale)
	if errors.Is(err, ErrOrderNotFound) {
		return View{Text: text(session.Locale, "error.notFound"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.orders"), routeOrders)))}
	}
	if err != nil {
		app.logger.Error("order lookup failed", "error", err)
		return app.errorView(session.Locale, routeOrders)
	}
	if !orderAwaitsPayment(order) {
		return app.orderScreen(ctx, session, orderID)
	}
	if !paymentChoiceOffered(app.orderPaymentChoices(order), provider, order.Currency) {
		return View{Text: text(session.Locale, "pay.none"), Keyboard: keyboard(row(actionButton(text(session.Locale, "action.back"), "order:"+orderID)))}
	}
	return app.startOrderPayment(ctx, session, order, provider)
}

func (app *App) startOrderPayment(ctx context.Context, session commerceContext, order OrderSummary, provider string) View {
	if _, err := app.commerce.StartPayment(ctx, order, provider, session.TelegramID, order.PlanName); err != nil {
		app.logger.Error("payment intent creation failed", "provider", provider, "error", err)
		return paymentStartFailedView(session.Locale, order.ID)
	}
	return app.orderScreen(ctx, session, order.ID)
}

// orderPaymentChoices lists the methods that can still settle an order: every
// enabled adapter that supports the order's currency, priced at what is left to
// pay. A method whose payment already failed, was cancelled, or expired for this
// order is left out — the shared payment service resumes an existing intent per
// order and provider, so offering it again would return the dead intent
// unchanged and the customer would tap a button that does nothing.
func (app *App) orderPaymentChoices(order OrderSummary) []PaymentChoice {
	offered := app.commerce.ExternalPaymentChoices(order.Currency)
	choices := make([]PaymentChoice, 0, len(offered))
	for _, choice := range offered {
		if choice.Provider == order.Provider && paymentIntentDead(order.PaymentStatus) {
			continue
		}
		choice.AmountMinor = order.ExternalMinor
		choices = append(choices, choice)
	}
	return choices
}

// orderAwaitsPayment reports whether a provider payment can still be attached
// to an order: it is pending, with an amount left after the wallet. The shared
// payment service refuses anything else, so the screen does not offer it.
func orderAwaitsPayment(order OrderSummary) bool {
	return order.State == commerce.OrderPending && order.ExternalMinor > 0
}

// paymentIntentDead reports whether a payment intent status is one nothing
// further can happen to.
func paymentIntentDead(status string) bool {
	switch status {
	case "failed", "cancelled", "expired":
		return true
	default:
		return false
	}
}

func paymentChoiceOffered(choices []PaymentChoice, provider, currency string) bool {
	for _, choice := range choices {
		if choice.Provider == provider && choice.Currency == currency {
			return true
		}
	}
	return false
}

func (app *App) ordersScreen(ctx context.Context, session commerceContext) View {
	orders, err := app.customers.Orders(ctx, session.Customer.ID, session.Locale, 10)
	if err != nil {
		app.logger.Error("order history lookup failed", "error", err)
		return app.errorView(session.Locale, routeOrders)
	}
	return ordersView(session.Locale, orders)
}

func (app *App) walletScreen(ctx context.Context, session commerceContext) View {
	balance, err := app.customers.WalletBalance(ctx, session.Customer.ID, app.settings.Currency)
	if err != nil {
		app.logger.Error("wallet balance lookup failed", "error", err)
		return app.errorView(session.Locale, routeWallet)
	}
	entries, err := app.customers.WalletHistory(ctx, session.Customer.ID, app.settings.Currency, 10)
	if err != nil {
		app.logger.Error("wallet history lookup failed", "error", err)
		return app.errorView(session.Locale, routeWallet)
	}
	return walletView(session.Locale, balance, app.settings.Currency, entries, app.commerce.TopUpLimits().Enabled)
}

func (app *App) newsScreen(ctx context.Context, session commerceContext) View {
	items, err := app.customers.News(ctx, session.Customer.ID, session.Locale, 10)
	if err != nil {
		app.logger.Error("news lookup failed", "error", err)
		return app.errorView(session.Locale, routeNews)
	}
	return newsListView(session.Locale, items)
}

func (app *App) newsItemScreen(ctx context.Context, session commerceContext, postID string) View {
	item, err := app.customers.NewsItem(ctx, session.Customer.ID, postID, session.Locale)
	if errors.Is(err, ErrNewsNotFound) {
		return View{Text: text(session.Locale, "news.gone"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routeNews)))}
	}
	if err != nil {
		app.logger.Error("news lookup failed", "error", err)
		return app.errorView(session.Locale, routeNews)
	}
	if err = app.customers.MarkNewsRead(ctx, session.Customer.ID, postID); err != nil {
		app.logger.Warn("news read state update failed", "error", err)
	}
	return newsItemView(session.Locale, item)
}

func (app *App) supportScreen(ctx context.Context, session commerceContext) View {
	tickets, err := app.customers.Tickets(ctx, session.Customer.ID, 10)
	if err != nil {
		app.logger.Error("support ticket lookup failed", "error", err)
		return app.errorView(session.Locale, routeSupport)
	}
	return supportListView(session.Locale, tickets, app.supportURL)
}

func (app *App) ticketScreen(ctx context.Context, session commerceContext, ticketID string) View {
	ticket, messages, err := app.customers.Ticket(ctx, session.Customer.ID, ticketID)
	if errors.Is(err, ErrTicketNotFound) {
		return View{Text: text(session.Locale, "support.notFound"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.support"), routeSupport)))}
	}
	if err != nil {
		app.logger.Error("support ticket lookup failed", "error", err)
		return app.errorView(session.Locale, routeSupport)
	}
	if err = app.customers.MarkTicketRead(ctx, session.Customer.ID, ticketID); err != nil {
		app.logger.Warn("support read state update failed", "error", err)
	}
	return supportTicketView(session.Locale, ticket, messages)
}

// autoRenewScreen is the settings entry point. Auto-renew is configured per
// subscription, so a customer holding several is asked which one first; a
// customer holding one — every single-subscription installation — goes straight
// to that subscription's screen.
func (app *App) autoRenewScreen(ctx context.Context, session commerceContext) View {
	if !app.commerce.SubscriptionPolicy().MultiEnabled {
		return app.autoRenewScreenFor(ctx, session, "")
	}
	subscriptions, err := app.customers.Subscriptions(ctx, session.Customer.ID, session.Locale)
	if err != nil {
		app.logger.Error("subscription lookup failed", "error", err)
		return app.errorView(session.Locale, routeSettings)
	}
	if len(subscriptions) > 1 {
		return autoRenewPickerView(session.Locale, subscriptions)
	}
	return app.autoRenewScreenFor(ctx, session, "")
}

// renewalTarget resolves which subscription an auto-renew screen or action is
// about. An explicit identifier is checked for ownership; none means the
// primary subscription, which is the only one a single-subscription
// installation ever has. A customer with no subscription at all gets an empty
// summary rather than an error: the screen then simply has nothing to switch on.
func (app *App) renewalTarget(ctx context.Context, session commerceContext, subscriptionID string) (SubscriptionSummary, error) {
	var (
		target SubscriptionSummary
		err    error
	)
	if subscriptionID != "" {
		target, err = app.customers.Subscription(ctx, session.Customer.ID, subscriptionID, session.Locale)
	} else {
		target, err = app.customers.PrimarySubscription(ctx, session.Customer.ID, session.Locale)
	}
	if errors.Is(err, ErrSubscriptionNotFound) {
		return SubscriptionSummary{}, nil
	}
	return target, err
}

// autoRenewScreenFor renders the recurring-billing screen of one subscription.
func (app *App) autoRenewScreenFor(ctx context.Context, session commerceContext, subscriptionID string) View {
	supported := commerce.SupportsAutoRenew(app.commerce.payments.Options(), app.settings.Currency)
	target, err := app.renewalTarget(ctx, session, subscriptionID)
	if err != nil {
		app.logger.Error("subscription lookup failed", "error", err)
		return app.errorView(session.Locale, routeSettings)
	}
	settings, err := app.customers.RenewalSettings(ctx, session.Customer.ID, target.ID)
	if err != nil {
		app.logger.Error("auto-renew lookup failed", "error", err)
		return app.errorView(session.Locale, routeSettings)
	}
	methods, err := app.customers.SavedMethods(ctx, session.Customer.ID)
	if err != nil {
		app.logger.Error("saved payment method lookup failed", "error", err)
		return app.errorView(session.Locale, routeSettings)
	}
	screen := autoRenewScreen{Settings: settings, Methods: methods, Supported: supported, BackRoute: routeSettings}
	if target.ID != "" {
		entitlement, entitlementErr := app.customers.EntitlementForSubscription(ctx, session.Customer.ID, target.ID, session.Locale, app.settings.Currency)
		if entitlementErr != nil {
			app.logger.Error("entitlement lookup failed", "error", entitlementErr)
			return app.errorView(session.Locale, routeSettings)
		}
		screen.PlanName = entitlement.PlanName
	}
	if app.commerce.SubscriptionPolicy().MultiEnabled && target.ID != "" {
		screen.SubscriptionLabel = target.Label
		screen.BackRoute = routeAutoRenew
	}
	return autoRenewSettingsView(session.Locale, screen)
}

func (app *App) savedMethodsScreen(ctx context.Context, session commerceContext) View {
	methods, err := app.customers.SavedMethods(ctx, session.Customer.ID)
	if err != nil {
		app.logger.Error("saved payment method lookup failed", "error", err)
		return app.errorView(session.Locale, routeSettings)
	}
	return savedMethodsView(session.Locale, methods)
}

// setRenewalFunding switches between the wallet and a saved method.
//
// Asking to charge a card that is not saved is a normal thing for a customer to
// try, so it gets its own message rather than a generic failure.
func (app *App) setRenewalFunding(ctx context.Context, session commerceContext, funding, subscriptionID string) View {
	target, err := app.renewalTarget(ctx, session, subscriptionID)
	if err != nil {
		app.logger.Error("subscription lookup failed", "error", err)
		return app.errorView(session.Locale, routeSettings)
	}
	err = app.customers.SetRenewalFunding(ctx, session.Customer.ID, target.ID, funding)
	if errors.Is(err, errNoSavedMethod) {
		return View{
			Text:     text(session.Locale, "renew.noMethod"),
			Keyboard: keyboard(row(actionButton(text(session.Locale, "action.back"), renewCallback("renew-settings", target.ID)))),
		}
	}
	if err != nil {
		app.logger.Error("renewal funding update failed", "error", err)
		return app.errorView(session.Locale, routeSettings)
	}
	return app.autoRenewScreenFor(ctx, session, target.ID)
}

func (app *App) setRenewalLeadTime(ctx context.Context, session commerceContext, days, subscriptionID string) View {
	target, err := app.renewalTarget(ctx, session, subscriptionID)
	if err != nil {
		app.logger.Error("subscription lookup failed", "error", err)
		return app.errorView(session.Locale, routeSettings)
	}
	parsed, err := strconv.Atoi(days)
	if err != nil || parsed <= 0 {
		return app.autoRenewScreenFor(ctx, session, target.ID)
	}
	if err := app.customers.SetRenewalLeadTime(
		ctx, session.Customer.ID, target.ID, time.Duration(parsed)*24*time.Hour,
	); err != nil {
		app.logger.Error("renewal lead time update failed", "error", err)
		return app.errorView(session.Locale, routeSettings)
	}
	return app.autoRenewScreenFor(ctx, session, target.ID)
}

// setDefaultMethod and removeMethod both address a method by an identifier that
// arrived in callback data. The store re-checks ownership on every write, so a
// forged identifier changes nothing rather than reaching another customer's
// card.
func (app *App) setDefaultMethod(ctx context.Context, session commerceContext, methodID string) View {
	if err := app.customers.SetDefaultMethod(ctx, session.Customer.ID, methodID); err != nil {
		app.logger.Warn("default payment method update failed", "error", err)
	}
	return app.savedMethodsScreen(ctx, session)
}

func (app *App) removeMethod(ctx context.Context, session commerceContext, methodID string) View {
	if err := app.customers.RemoveMethod(ctx, session.Customer.ID, methodID); err != nil {
		app.logger.Warn("payment method removal failed", "error", err)
	}
	return app.savedMethodsScreen(ctx, session)
}

// handleCommerceAction runs the callback actions the commerce surface owns.
func (app *App) handleCommerceAction(ctx context.Context, session commerceContext, parts []string) (View, bool) {
	if !app.commerceEnabled() {
		return View{}, false
	}
	argument := ""
	if len(parts) > 1 {
		argument = parts[1]
	}
	if view, handled := app.handleExpansionAction(ctx, session, parts); handled {
		return view, true
	}
	if view, handled := app.handleShopAction(ctx, session, parts); handled {
		return view, true
	}
	if view, handled := app.handleGiftAction(ctx, session, parts); handled {
		return view, true
	}
	if view, handled := app.handleOfferAction(ctx, session, parts); handled {
		return view, true
	}
	switch parts[0] {
	case "plan":
		return app.planScreen(ctx, session, argument, argumentAt(parts, 2) == newSubscriptionFlag), true
	case "buy":
		if len(parts) != 3 && len(parts) != 4 {
			return app.errorView(session.Locale, routePlans), true
		}
		return app.paymentMethodScreen(ctx, session, parts[1], parts[2], "", argumentAt(parts, 3) == newSubscriptionFlag), true
	case "pm":
		if len(parts) != 3 {
			return app.errorView(session.Locale, routePlans), true
		}
		return app.selectProvider(ctx, session, parts[1], parts[2]), true
	case "checkout":
		return app.checkoutScreen(ctx, session), true
	case "confirm":
		return app.confirmCheckout(ctx, session), true
	case "promo":
		return app.beginPromo(ctx, session), true
	case "promo-clear":
		return app.clearPromo(ctx, session), true
	case "wallet-toggle":
		return app.toggleWallet(ctx, session), true
	case "order":
		return app.orderScreen(ctx, session, argument), true
	case "pay":
		return app.payOrder(ctx, session, argument, argumentAt(parts, 2) == "pick"), true
	case "order-pm":
		if len(parts) != 3 {
			return app.errorView(session.Locale, routeOrders), true
		}
		return app.selectOrderProvider(ctx, session, parts[1], parts[2]), true
	case "order-cancel":
		return app.cancelOrder(ctx, session, argument), true
	case "news":
		return app.newsItemScreen(ctx, session, argument), true
	case "ticket":
		return app.ticketScreen(ctx, session, argument), true
	case "ticket-reply":
		return app.beginTicketReply(ctx, session, argument), true
	case "ticket-close":
		return app.setTicketStatus(ctx, session, argument, "closed"), true
	case "ticket-open":
		return app.setTicketStatus(ctx, session, argument, "open"), true
	case "support-new":
		return app.beginTicketReply(ctx, session, ""), true
	case "connect":
		return app.connectPlatform(ctx, session, argument), true
	case "autorenew":
		return app.setAutoRenew(ctx, session, argument == "on", argumentAt(parts, 2)), true
	case "renew-settings":
		return app.autoRenewScreenFor(ctx, session, argument), true
	case "renew-funding":
		return app.setRenewalFunding(ctx, session, argument, argumentAt(parts, 2)), true
	case "renew-lead":
		return app.setRenewalLeadTime(ctx, session, argument, argumentAt(parts, 2)), true
	case "method-default":
		return app.setDefaultMethod(ctx, session, argument), true
	case "method-remove":
		return app.removeMethod(ctx, session, argument), true
	case "quiet":
		return app.setQuietHours(ctx, session, argument), true
	default:
		return View{}, false
	}
}

func (app *App) selectProvider(ctx context.Context, session commerceContext, provider, currency string) View {
	checkout, found, err := app.customers.Checkout(ctx, session.Customer.ID)
	if err != nil || !found {
		return View{Text: text(session.Locale, "checkout.expired"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.plans"), routePlans)))}
	}
	choices, err := app.commerce.PaymentChoices(ctx, checkout.PlanVersionID)
	if err != nil {
		app.logger.Error("payment method lookup failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	// The chosen method must still be one of the offered ones: callback data is
	// customer-controlled and cannot be trusted to name a compatible adapter.
	valid := false
	for _, choice := range choices {
		if choice.Provider == provider && choice.Currency == currency {
			valid = true
			break
		}
	}
	if !valid {
		return View{Text: text(session.Locale, "pay.none"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.plans"), routePlans)))}
	}
	if _, err = app.customers.SetCheckoutProvider(ctx, checkout.ID, provider, currency); err != nil {
		app.logger.Error("checkout provider selection failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	return app.checkoutScreen(ctx, session)
}

func (app *App) beginPromo(ctx context.Context, session commerceContext) View {
	if err := app.customers.BeginSessionState(ctx, session.TelegramID, "promo_code", nil); err != nil {
		app.logger.Error("promo prompt failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	return promoPromptView(session.Locale)
}

func (app *App) clearPromo(ctx context.Context, session commerceContext) View {
	checkout, found, err := app.customers.Checkout(ctx, session.Customer.ID)
	if err != nil || !found {
		return View{Text: text(session.Locale, "checkout.expired"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.plans"), routePlans)))}
	}
	if _, err = app.customers.SetCheckoutPromo(ctx, checkout.ID, "", ""); err != nil {
		app.logger.Error("promo removal failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	return app.checkoutScreen(ctx, session)
}

func (app *App) toggleWallet(ctx context.Context, session commerceContext) View {
	checkout, found, err := app.customers.Checkout(ctx, session.Customer.ID)
	if err != nil || !found {
		return View{Text: text(session.Locale, "checkout.expired"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.plans"), routePlans)))}
	}
	if _, err = app.customers.SetCheckoutWallet(ctx, checkout.ID, !checkout.ApplyWallet); err != nil {
		app.logger.Error("wallet preference update failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	return app.checkoutScreen(ctx, session)
}

func (app *App) cancelOrder(ctx context.Context, session commerceContext, orderID string) View {
	if err := app.customers.CancelOrder(ctx, session.Customer.ID, orderID); err != nil {
		app.logger.Warn("order cancellation rejected", "error", err)
	}
	return app.orderScreen(ctx, session, orderID)
}

func (app *App) beginTicketReply(ctx context.Context, session commerceContext, ticketID string) View {
	context := map[string]any{}
	if ticketID != "" {
		context["ticketId"] = ticketID
	}
	if err := app.customers.BeginSessionState(ctx, session.TelegramID, "support_reply", context); err != nil {
		app.logger.Error("support compose failed", "error", err)
		return app.errorView(session.Locale, routeSupport)
	}
	return supportComposeTicketView(session.Locale, ticketID)
}

func (app *App) setTicketStatus(ctx context.Context, session commerceContext, ticketID, status string) View {
	err := app.customers.SetTicketStatus(ctx, session.Customer.ID, ticketID, status)
	if view, final := supportSubmitOutcome(session.Locale, err); final && !errors.Is(err, ErrTicketNotFound) {
		// A merged or closed conversation explains itself rather than showing a
		// generic failure for a button that was stale when it was pressed.
		return view
	}
	if err != nil && !errors.Is(err, ErrTicketNotFound) {
		app.logger.Error("support ticket status update failed", "error", err)
		return app.errorView(session.Locale, routeSupport)
	}
	return app.ticketScreen(ctx, session, ticketID)
}

func (app *App) connectPlatform(ctx context.Context, session commerceContext, platform string) View {
	subscription, err := app.subscriptionFor(ctx, session)
	if err != nil {
		app.logger.Warn("subscription lookup failed", "error", err)
		return app.errorView(session.Locale, routeConnect)
	}
	return app.connectPlatformScreen(ctx, session.Locale, platform, subscription)
}

// setAutoRenew arms or disarms automatic renewal for one subscription. The
// subscription is part of the setting's key and is what the renewal worker
// joins the entitlement on, so it is resolved before anything is written.
func (app *App) setAutoRenew(ctx context.Context, session commerceContext, enabled bool, subscriptionID string) View {
	target, err := app.renewalTarget(ctx, session, subscriptionID)
	if err != nil {
		app.logger.Error("subscription lookup failed", "error", err)
		return app.errorView(session.Locale, routeSettings)
	}
	setting := AutoRenew{Enabled: enabled, SubscriptionID: target.ID}
	if enabled {
		if target.ID == "" {
			return app.autoRenewScreenFor(ctx, session, "")
		}
		entitlement, err := app.customers.EntitlementForSubscription(ctx, session.Customer.ID, target.ID, session.Locale, app.settings.Currency)
		if err != nil || !entitlement.Found {
			return app.autoRenewScreenFor(ctx, session, target.ID)
		}
		choices, err := app.commerce.PaymentChoices(ctx, entitlement.PlanVersionID)
		if err != nil {
			app.logger.Error("payment method lookup failed", "error", err)
			return app.errorView(session.Locale, routeSettings)
		}
		for _, choice := range choices {
			if choice.Recurring {
				setting.PlanVersionID, setting.Provider, setting.Currency = entitlement.PlanVersionID, choice.Provider, choice.Currency
				break
			}
		}
		if setting.Provider == "" {
			return app.autoRenewScreenFor(ctx, session, target.ID)
		}
	}
	if err := app.customers.SetAutoRenew(ctx, session.Customer.ID, setting); err != nil {
		app.logger.Error("auto-renew update failed", "error", err)
		return app.errorView(session.Locale, routeSettings)
	}
	return app.autoRenewScreenFor(ctx, session, target.ID)
}

func (app *App) setQuietHours(ctx context.Context, session commerceContext, window string) View {
	start, end, err := parseQuietWindow(window)
	if err != nil {
		return app.errorView(session.Locale, routeSettings)
	}
	if err = app.customers.SetQuietHours(ctx, session.Customer.ID, start, end); err != nil {
		app.logger.Error("quiet hours update failed", "error", err)
		return app.errorView(session.Locale, routeSettings)
	}
	return app.settingsScreen(ctx, session)
}

func parseQuietWindow(window string) (int, int, error) {
	parts := strings.Split(window, "-")
	if len(parts) != 2 {
		return 0, 0, errors.New("invalid quiet window")
	}
	start, startErr := strconv.Atoi(parts[0])
	end, endErr := strconv.Atoi(parts[1])
	if startErr != nil || endErr != nil {
		return 0, 0, errors.New("invalid quiet window")
	}
	return start, end, nil
}

// settingsScreen renders the v0.4 settings surface with every communication
// control the customer owns.
func (app *App) settingsScreen(ctx context.Context, session commerceContext) View {
	preferences, err := app.customers.CustomerPreferences(ctx, session.Customer.ID)
	if err != nil {
		app.logger.Error("preferences lookup failed", "error", err)
		return app.errorView(session.Locale, routeSettings)
	}
	return customerSettingsView(session.Locale, preferences, app.settings.MarketingFrequencyCap)
}

// subscriptionFor reads the customer's Remnawave subscription. A customer with
// no provisioned VPN user yet is not an error: the connect screen explains that
// the link appears once access is active.
func (app *App) subscriptionFor(ctx context.Context, session commerceContext) (remnawave.Subscription, error) {
	if session.Customer.RemnawaveID <= 0 {
		return remnawave.Subscription{}, nil
	}
	subscription, err := app.remnawave.Subscription(ctx, session.Customer.RemnawaveID)
	if errors.Is(err, remnawave.ErrNotFound) {
		return remnawave.Subscription{}, nil
	}
	return subscription, err
}

func (app *App) errorView(locale Locale, route string) View {
	return View{Text: text(locale, "error.load"), Keyboard: retryKeyboard(locale, route), RetryRoute: route}
}

// StarsInvoice sends a Telegram Stars invoice for an order. The invoice payload
// is the order identifier, which is what settlement matches against.
func (app *App) StarsInvoice(ctx context.Context, client *telegram.Bot, session commerceContext, chatID int64, orderID string) error {
	order, err := app.customers.Order(ctx, session.Customer.ID, orderID, session.Locale)
	if err != nil {
		return err
	}
	if order.Currency != "XTR" || order.ExternalMinor <= 0 {
		return errors.New("order is not payable in Telegram Stars")
	}
	_, err = client.SendInvoice(ctx, &telegram.SendInvoiceParams{
		ChatID:      chatID,
		Title:       text(session.Locale, "payment.starsTitle"),
		Description: text(session.Locale, "payment.starsDescription", order.PlanName, formatMoney(order.ExternalMinor, order.Currency)),
		Payload:     order.ID,
		Currency:    "XTR",
		Prices: []models.LabeledPrice{{
			Label:  truncateRunes(order.PlanName, 32),
			Amount: int(order.ExternalMinor),
		}},
		ProtectContent: true,
	})
	return err
}

// HandlePreCheckout answers Telegram's pre-checkout probe. An order that is no
// longer payable is rejected before the customer is charged.
func (app *App) HandlePreCheckout(ctx context.Context, client *telegram.Bot, query *models.PreCheckoutQuery) {
	if !app.commerceEnabled() || query == nil || query.From == nil {
		return
	}
	locale := localeFrom(query.From.LanguageCode)
	approve := false
	session, err := app.commerceContext(ctx, query.From.ID, query.From.LanguageCode, query.From.Username)
	if err == nil {
		locale = session.Locale
		order, orderErr := app.customers.Order(ctx, session.Customer.ID, query.InvoicePayload, locale)
		approve = orderErr == nil && order.Currency == query.Currency &&
			order.ExternalMinor == int64(query.TotalAmount) &&
			(order.State == commerce.OrderPending || order.State == commerce.OrderDraft)
	}
	message := ""
	if !approve {
		message = text(locale, "payment.precheckoutStale")
	}
	if _, err := client.AnswerPreCheckoutQuery(ctx, &telegram.AnswerPreCheckoutQueryParams{PreCheckoutQueryID: query.ID, OK: approve, ErrorMessage: message}); err != nil {
		app.logger.Error("pre-checkout answer failed", "error", err)
	}
}

// HandleSuccessfulPayment settles a Telegram Stars payment received on the
// authenticated update stream and shows the resulting order state.
func (app *App) HandleSuccessfulPayment(ctx context.Context, client *telegram.Bot, update *models.Update) {
	message := update.Message
	if !app.commerceEnabled() || message == nil || message.From == nil || message.SuccessfulPayment == nil {
		return
	}
	payment := message.SuccessfulPayment
	logger := app.logger.With("correlation_id", correlationID(update), "provider", "telegram_stars")
	session, err := app.commerceContext(ctx, message.From.ID, message.From.LanguageCode, message.From.Username)
	if err != nil {
		logger.Error("customer resolution failed during settlement", "error", err)
		return
	}
	if err := app.commerce.SettleStars(ctx, payment.InvoicePayload, payment.TelegramPaymentChargeID, int64(payment.TotalAmount), int64(update.ID)); err != nil {
		logger.Error("Telegram Stars settlement failed", "error", err)
		_ = app.sender.Send(ctx, session.Customer.ID, message.Chat.ID, View{Text: text(session.Locale, "error.payment"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.orders"), routeOrders)))})
		return
	}
	logger.Info("Telegram Stars payment settled", "order_id", payment.InvoicePayload)
	view := app.orderScreen(ctx, session, payment.InvoicePayload)
	if err := app.sender.Send(ctx, session.Customer.ID, message.Chat.ID, view); err != nil {
		logger.Error("settlement confirmation delivery failed", "error", err)
	}
}

// correlationID derives one identifier that ties a Telegram update to every
// order, payment, job, and Remnawave call it causes.
func correlationID(update *models.Update) string {
	if update == nil {
		return "tg:unknown"
	}
	return "tg:" + strconv.FormatInt(int64(update.ID), 10)
}

// withCorrelation returns a logger tagged with the update correlation ID.
func (app *App) withCorrelation(update *models.Update) *slog.Logger {
	return app.logger.With("correlation_id", correlationID(update))
}

// dispatchCommerceAction resolves the caller, runs a commerce action, and edits
// the screen in place. It reports whether the action belonged to this surface.
func (app *App) dispatchCommerceAction(ctx context.Context, client *telegram.Bot, query *models.CallbackQuery, chatID int64, messageID int, parts []string, update *models.Update) bool {
	if !app.commerceEnabled() || !commerceActions[parts[0]] {
		return false
	}
	logger := app.withCorrelation(update).With("action", parts[0])
	session, err := app.commerceContext(ctx, query.From.ID, query.From.LanguageCode, query.From.Username)
	if err != nil {
		logger.Error("customer resolution failed", "error", err)
		return false
	}
	if parts[0] == "invoice" {
		if err := app.StarsInvoice(ctx, client, session, chatID, argumentOf(parts)); err != nil {
			logger.Error("Telegram Stars invoice failed", "error", err)
			_, _ = client.SendMessage(ctx, sendParams(chatID, View{Text: text(session.Locale, "error.payment")}))
		}
		return true
	}
	if parts[0] == "quiet-menu" {
		preferences, prefErr := app.customers.CustomerPreferences(ctx, session.Customer.ID)
		if prefErr != nil {
			logger.Error("preferences lookup failed", "error", prefErr)
			return true
		}
		app.replaceScreen(ctx, client, chatID, messageID, quietHoursView(session.Locale, preferences), logger)
		return true
	}
	if _, err := client.EditMessageText(ctx, editParams(chatID, messageID, loadingView(session.Locale))); err != nil {
		logger.Debug("telegram loading state edit failed", "error", err)
	}
	view, handled := app.handleCommerceAction(ctx, session, parts)
	if !handled {
		return false
	}
	app.replaceScreen(ctx, client, chatID, messageID, view, logger)
	return true
}

func (app *App) replaceScreen(ctx context.Context, client *telegram.Bot, chatID int64, messageID int, view View, logger *slog.Logger) {
	if _, err := client.EditMessageText(ctx, editParams(chatID, messageID, view)); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "message is not modified") {
			return
		}
		logger.Error("telegram view edit failed", "error", err)
	}
}

func argumentOf(parts []string) string {
	return argumentAt(parts, 1)
}

// argumentAt reads one positional callback argument, or "" when the callback
// did not carry it.
func argumentAt(parts []string, index int) string {
	if len(parts) > index {
		return parts[index]
	}
	return ""
}

// commerceActions is the closed set of callback actions the commerce surface
// owns. Anything else falls through to the v0.2 handlers.
//
// Every action a commerce screen renders has to be listed here: an action that
// is handled below but missing from this set never reaches its handler, and
// the customer sees "Action was not completed" instead.
var commerceActions = func() map[string]bool {
	actions := map[string]bool{
		"plan": true, "buy": true, "pm": true, "checkout": true, "confirm": true,
		"promo": true, "promo-clear": true, "wallet-toggle": true, "order": true,
		"pay": true, "order-pm": true,
		"order-cancel": true, "news": true, "ticket": true, "ticket-reply": true,
		"ticket-close": true, "ticket-open": true, "support-new": true, "connect": true,
		"autorenew": true, "renew-settings": true, "renew-funding": true, "renew-lead": true,
		"method-default": true, "method-remove": true,
		"quiet": true, "quiet-menu": true, "invoice": true,
	}
	for _, owned := range []map[string]bool{expansionActions, giftActions, shopActions, offerActions} {
		for action := range owned {
			actions[action] = true
		}
	}
	return actions
}()
