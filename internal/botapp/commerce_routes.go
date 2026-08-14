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
	return state
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
		return app.plansScreen(ctx, session), true
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

func (app *App) plansScreen(ctx context.Context, session commerceContext) View {
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
	return plansView(session.Locale, plans, app.settings.Currency)
}

func (app *App) planScreen(ctx context.Context, session commerceContext, planVersionID string) View {
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
	return planView(session.Locale, plan, entitlement, app.termsURL, trialReason)
}

// paymentMethodScreen starts a checkout and asks which configured adapter should
// settle it. Choosing the method is what fixes the order currency.
func (app *App) paymentMethodScreen(ctx context.Context, session commerceContext, planVersionID, operation, subscriptionID string) View {
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
	target, err := app.checkoutTarget(ctx, session, normalized, subscriptionID)
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
	return paymentMethodView(session.Locale, plan, choices)
}

// checkoutTarget resolves which subscription a checkout changes. An explicit
// identifier always wins; otherwise a change targets the primary subscription
// and a purchase opens a new one only when concurrency is enabled.
func (app *App) checkoutTarget(ctx context.Context, session commerceContext, operation, subscriptionID string) (string, error) {
	if subscriptionID != "" {
		if _, err := app.customers.Subscription(ctx, session.Customer.ID, subscriptionID, session.Locale); err != nil {
			return "", err
		}
		return subscriptionID, nil
	}
	if commerce.TargetsNewSubscription(operation) && app.commerce.SubscriptionPolicy().MultiEnabled {
		return "", nil
	}
	primary, err := app.customers.PrimarySubscription(ctx, session.Customer.ID, session.Locale)
	if errors.Is(err, ErrSubscriptionNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return primary.ID, nil
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
		return channelGateView(session.Locale, gate)
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
		return View{Text: text(session.Locale, "error.payment"), Keyboard: keyboard(row(actionButton(text(session.Locale, "action.retry"), "order:"+order.ID)), row(callbackButton(text(session.Locale, "menu.orders"), routeOrders)))}
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
	return orderStatusView(session.Locale, order, refunds)
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

func (app *App) autoRenewScreen(ctx context.Context, session commerceContext) View {
	settings, err := app.customers.RenewalSettings(ctx, session.Customer.ID)
	if err != nil {
		app.logger.Error("auto-renew lookup failed", "error", err)
		return app.errorView(session.Locale, routeSettings)
	}
	methods, err := app.customers.SavedMethods(ctx, session.Customer.ID)
	if err != nil {
		app.logger.Error("saved payment method lookup failed", "error", err)
		return app.errorView(session.Locale, routeSettings)
	}
	entitlement, err := app.customers.Entitlement(ctx, session.Customer.ID, session.Locale, app.settings.Currency)
	if err != nil {
		app.logger.Error("entitlement lookup failed", "error", err)
		return app.errorView(session.Locale, routeSettings)
	}
	supported := commerce.SupportsAutoRenew(app.commerce.payments.Options(), app.settings.Currency)
	return autoRenewSettingsView(session.Locale, settings, methods, entitlement.PlanName, supported)
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
func (app *App) setRenewalFunding(ctx context.Context, session commerceContext, funding string) View {
	err := app.customers.SetRenewalFunding(ctx, session.Customer.ID, funding)
	if errors.Is(err, errNoSavedMethod) {
		return View{
			Text:     text(session.Locale, "renew.noMethod"),
			Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routeAutoRenew))),
		}
	}
	if err != nil {
		app.logger.Error("renewal funding update failed", "error", err)
		return app.errorView(session.Locale, routeSettings)
	}
	return app.autoRenewScreen(ctx, session)
}

func (app *App) setRenewalLeadTime(ctx context.Context, session commerceContext, days string) View {
	parsed, err := strconv.Atoi(days)
	if err != nil || parsed <= 0 {
		return app.autoRenewScreen(ctx, session)
	}
	if err := app.customers.SetRenewalLeadTime(
		ctx, session.Customer.ID, time.Duration(parsed)*24*time.Hour,
	); err != nil {
		app.logger.Error("renewal lead time update failed", "error", err)
		return app.errorView(session.Locale, routeSettings)
	}
	return app.autoRenewScreen(ctx, session)
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
		return app.planScreen(ctx, session, argument), true
	case "buy":
		if len(parts) != 3 {
			return app.errorView(session.Locale, routePlans), true
		}
		return app.paymentMethodScreen(ctx, session, parts[1], parts[2], ""), true
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
		return app.setAutoRenew(ctx, session, argument == "on"), true
	case "renew-funding":
		return app.setRenewalFunding(ctx, session, argument), true
	case "renew-lead":
		return app.setRenewalLeadTime(ctx, session, argument), true
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
	if err := app.customers.SetTicketStatus(ctx, session.Customer.ID, ticketID, status); err != nil && !errors.Is(err, ErrTicketNotFound) {
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

func (app *App) setAutoRenew(ctx context.Context, session commerceContext, enabled bool) View {
	setting := AutoRenew{Enabled: enabled}
	if enabled {
		entitlement, err := app.customers.Entitlement(ctx, session.Customer.ID, session.Locale, app.settings.Currency)
		if err != nil || !entitlement.Found {
			return app.autoRenewScreen(ctx, session)
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
			return app.autoRenewScreen(ctx, session)
		}
	}
	if err := app.customers.SetAutoRenew(ctx, session.Customer.ID, setting); err != nil {
		app.logger.Error("auto-renew update failed", "error", err)
		return app.errorView(session.Locale, routeSettings)
	}
	return app.autoRenewScreen(ctx, session)
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
	if len(parts) > 1 {
		return parts[1]
	}
	return ""
}

// commerceActions is the closed set of callback actions the commerce surface
// owns. Anything else falls through to the v0.2 handlers.
var commerceActions = func() map[string]bool {
	actions := map[string]bool{
		"plan": true, "buy": true, "pm": true, "checkout": true, "confirm": true,
		"promo": true, "promo-clear": true, "wallet-toggle": true, "order": true,
		"order-cancel": true, "news": true, "ticket": true, "ticket-reply": true,
		"ticket-close": true, "ticket-open": true, "support-new": true, "connect": true,
		"autorenew": true, "quiet": true, "quiet-menu": true, "invoice": true,
	}
	for action := range expansionActions {
		actions[action] = true
	}
	return actions
}()
