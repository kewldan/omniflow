package botapp

import (
	"context"
	"errors"
	"html"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// noPackage stands for "no traffic package" in a callback, where an add-on
// version would otherwise go.
const noPackage = "-"

// TrafficPackages lists the traffic packages a subscription may switch to.
func (service *Commerce) TrafficPackages(ctx context.Context, customerID, subscriptionID string, locale Locale) (accountcheckout.TrafficPackages, error) {
	return service.checkout.TrafficPackages(ctx, customerID, subscriptionID, string(locale))
}

// withRowBeforeLast adds one button row to a screen just above its last row,
// which is the way back.
func withRowBeforeLast(view View, button []models.InlineKeyboardButton) View {
	if view.Keyboard == nil || len(view.Keyboard.InlineKeyboard) == 0 {
		return view
	}
	rows := view.Keyboard.InlineKeyboard
	updated := make([][]models.InlineKeyboardButton, 0, len(rows)+1)
	updated = append(updated, rows[:len(rows)-1]...)
	updated = append(updated, button, rows[len(rows)-1])
	view.Keyboard = keyboard(updated...)
	return view
}

// withCapacityButtons offers the mid-period changes a subscription can make.
func (app *App) withCapacityButtons(ctx context.Context, session commerceContext, subscription SubscriptionSummary, view View) View {
	if !subscription.Found || app.commerce == nil || app.commerce.checkout == nil {
		return view
	}
	slot := strconv.Itoa(subscription.Slot)
	if packages, err := app.commerce.TrafficPackages(ctx, session.Customer.ID, subscription.ID, session.Locale); err == nil && packages.Available {
		view = withRowBeforeLast(view, row(actionButton(text(session.Locale, "capacity.trafficButton"), "tpk:"+slot)))
	}
	return view
}

// handleCapacityAction runs the mid-period capacity change callbacks.
func (app *App) handleCapacityAction(ctx context.Context, session commerceContext, parts []string) View {
	switch parts[0] {
	case "tpk":
		if len(parts) != 2 {
			return app.errorView(session.Locale, routeSubscriptions)
		}
		return app.trafficPackagesScreen(ctx, session, parts[1])
	case "tpk-ask", "tpk-buy":
		if len(parts) != 3 {
			return app.errorView(session.Locale, routeSubscriptions)
		}
		target := parts[2]
		if target == noPackage {
			target = ""
		}
		if parts[0] == "tpk-ask" {
			return app.trafficPackageConfirmScreen(ctx, session, parts[1], target)
		}
		return app.changeTrafficPackage(ctx, session, parts[1], target)
	}
	return app.errorView(session.Locale, routeSubscriptions)
}

// capacitySubscription resolves a callback's slot to this customer's own
// subscription.
func (app *App) capacitySubscription(ctx context.Context, session commerceContext, slot string) (SubscriptionSummary, bool) {
	number, err := strconv.Atoi(slot)
	if err != nil || app.commerce == nil || app.commerce.checkout == nil || app.commerce.orders == nil {
		return SubscriptionSummary{}, false
	}
	subscription, err := app.customers.SubscriptionBySlot(ctx, session.Customer.ID, number, session.Locale)
	return subscription, err == nil && subscription.Found
}

func (app *App) trafficPackagesScreen(ctx context.Context, session commerceContext, slot string) View {
	subscription, ok := app.capacitySubscription(ctx, session, slot)
	if !ok {
		return capacityUnavailableView(session.Locale, "", "capacity.trafficUnavailable")
	}
	packages, err := app.commerce.TrafficPackages(ctx, session.Customer.ID, subscription.ID, session.Locale)
	if err != nil {
		app.logger.Error("traffic packages lookup failed", "error", err)
		return app.errorView(session.Locale, routeSubscriptions)
	}
	if !packages.Available {
		return capacityUnavailableView(session.Locale, subscription.ID, "capacity.trafficUnavailable")
	}
	return trafficPackagesView(session.Locale, subscription, packages, slot)
}

func (app *App) trafficPackageConfirmScreen(ctx context.Context, session commerceContext, slot, addonVersionID string) View {
	subscription, ok := app.capacitySubscription(ctx, session, slot)
	if !ok {
		return capacityUnavailableView(session.Locale, "", "capacity.trafficUnavailable")
	}
	packages, err := app.commerce.TrafficPackages(ctx, session.Customer.ID, subscription.ID, session.Locale)
	if err != nil {
		app.logger.Error("traffic packages lookup failed", "error", err)
		return app.errorView(session.Locale, routeSubscriptions)
	}
	for _, option := range packages.Options {
		if option.AddonVersionID == addonVersionID {
			target := addonVersionID
			if target == "" {
				target = noPackage
			}
			name := option.Name
			if addonVersionID == "" {
				name = text(session.Locale, "capacity.trafficNone")
			}
			return capacityConfirmView(session.Locale, subscription,
				text(session.Locale, "capacity.trafficConfirm", html.EscapeString(name), formatBytes(option.TrafficBytes)),
				option.Quote, "tpk-buy:"+slot+":"+target)
		}
	}
	return capacityUnavailableView(session.Locale, subscription.ID, "capacity.trafficUnavailable")
}

// changeTrafficPackage opens the traffic_change order. The wallet settles it
// when it can; otherwise the customer picks a payment method, exactly as for
// an add-on.
func (app *App) changeTrafficPackage(ctx context.Context, session commerceContext, slot, addonVersionID string) View {
	subscription, ok := app.capacitySubscription(ctx, session, slot)
	if !ok {
		return capacityUnavailableView(session.Locale, "", "capacity.trafficUnavailable")
	}
	if gate := app.checkPurchaseChannels(ctx, session.Customer.ID, session.TelegramID); !gate.Allowed() {
		target := addonVersionID
		if target == "" {
			target = noPackage
		}
		return channelGateView(session.Locale, gate, "tpk-ask:"+slot+":"+target)
	}
	key, err := keyOrNew(session.TapKey)
	if err != nil {
		return app.errorView(session.Locale, routeSubscriptions)
	}
	order, err := app.commerce.orders.CreateTrafficChangeOrder(ctx, commercepg.CapacityChangeInput{
		CustomerID: session.Customer.ID, SubscriptionID: subscription.ID, Currency: app.settings.Currency,
		AddonVersionID: addonVersionID, IdempotencyKey: "bot-traffic:" + key,
	})
	if errors.Is(err, commerce.ErrTrafficChangeUnavailable) || errors.Is(err, commerce.ErrCapacityChangeSame) ||
		errors.Is(err, commercepg.ErrAddonUnavailable) || errors.Is(err, commercepg.ErrNoActiveSubscription) {
		return capacityUnavailableView(session.Locale, subscription.ID, "capacity.trafficUnavailable")
	}
	if err != nil {
		app.logger.Error("traffic change order failed", "error", err)
		return app.errorView(session.Locale, routeSubscriptions)
	}
	return app.capacityOrderView(ctx, session, uuidText(order.ID))
}

// capacityOrderView shows a settled change, or the payment-method choice for
// one the wallet did not cover.
func (app *App) capacityOrderView(ctx context.Context, session commerceContext, orderID string) View {
	summary, err := app.customers.Order(ctx, session.Customer.ID, orderID, session.Locale)
	if err != nil {
		app.logger.Error("capacity change order lookup failed", "error", err)
		return app.errorView(session.Locale, routeOrders)
	}
	if summary.ExternalMinor == 0 {
		return orderStatusView(session.Locale, summary, nil)
	}
	return orderPaymentMethodView(session.Locale, summary, app.saveableChoices(ctx, app.orderPaymentChoices(ctx, session.Customer.ID, summary)))
}

func capacityUnavailableView(locale Locale, subscriptionID, key string) View {
	back := callbackButton(text(locale, "subs.back"), routeSubscriptions)
	if subscriptionID != "" {
		back = actionButton(text(locale, "action.back"), "sub:"+subscriptionID)
	}
	return View{Text: text(locale, key), Keyboard: keyboard(row(back))}
}

func trafficPackagesView(locale Locale, subscription SubscriptionSummary, packages accountcheckout.TrafficPackages, slot string) View {
	body := &strings.Builder{}
	body.WriteString(text(locale, "capacity.trafficTitle"))
	body.WriteString("\n\n")
	body.WriteString(text(locale, "subs.detail", html.EscapeString(subscription.Label)))
	body.WriteString("\n\n")
	if packages.CurrentTrafficBytes > 0 {
		body.WriteString(text(locale, "capacity.trafficCurrent", formatBytes(packages.CurrentTrafficBytes)))
	} else {
		body.WriteString(text(locale, "capacity.trafficCurrentNone"))
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(packages.Options)+1)
	for _, option := range packages.Options {
		target, label := option.AddonVersionID, option.Name
		if target == "" {
			target, label = noPackage, text(locale, "capacity.trafficNone")
		} else {
			label += " · " + formatBytes(option.TrafficBytes)
		}
		arrow := "⬆️ "
		if option.Direction == "down" {
			arrow = "⬇️ "
		}
		rows = append(rows, row(actionButton(arrow+label, "tpk-ask:"+slot+":"+target)))
	}
	rows = append(rows, row(actionButton(text(locale, "action.back"), "sub:"+subscription.ID)))
	return View{Text: body.String(), Keyboard: keyboard(rows...)}
}

// capacityConfirmView is the preview of one capacity change: what is
// charged, what is credited, where the money comes from, and what goes back
// to the wallet, before the one tap that commits it.
func capacityConfirmView(locale Locale, subscription SubscriptionSummary, summary string, quote commercepg.CapacityChangeQuote, confirm string) View {
	money := func(minor int64) string { return formatMoney(minor, quote.Currency) }
	lines := []string{
		text(locale, "capacity.confirmTitle"), "",
		text(locale, "subs.detail", html.EscapeString(subscription.Label)), "",
		summary, "",
	}
	if quote.ChargeMinor > 0 {
		lines = append(lines, text(locale, "capacity.charge", money(quote.ChargeMinor)))
	}
	if quote.CreditMinor > 0 {
		lines = append(lines, text(locale, "capacity.credit", money(quote.CreditMinor)))
	}
	if quote.WalletMinor > 0 {
		lines = append(lines, text(locale, "capacity.wallet", money(quote.WalletMinor)))
	}
	if quote.ExternalMinor > 0 {
		lines = append(lines, text(locale, "capacity.external", money(quote.ExternalMinor)))
	} else {
		lines = append(lines, text(locale, "capacity.nothingDue"))
	}
	if quote.CreditToWalletMinor > 0 {
		lines = append(lines, text(locale, "capacity.toWallet", money(quote.CreditToWalletMinor)))
	}
	return View{Text: strings.Join(lines, "\n"), Keyboard: keyboard(
		row(actionButton(text(locale, "capacity.confirm"), confirm)),
		row(actionButton(text(locale, "action.cancel"), "sub:"+subscription.ID)),
	)}
}
