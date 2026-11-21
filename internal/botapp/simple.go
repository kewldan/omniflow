package botapp

import (
	"context"
	"errors"
	"html"
	"strings"

	"github.com/omniflow/omniflow/internal/accountcheckout"
)

// The simple subscription in the chat: with the operator's setting on, the
// plans screen is one plan and one Buy button, and the button opens the
// checkout with the period, traffic, and devices already chosen, straight to
// the payment method — no catalogue and no configurator.

// simplePlansScreen is the plans screen when the simple subscription is on;
// it reports false when it is off, so the catalogue is shown instead.
func (app *App) simplePlansScreen(ctx context.Context, session commerceContext) (View, bool) {
	if app.commerce == nil || app.commerce.checkout == nil {
		return View{}, false
	}
	offer, err := app.commerce.checkout.SimpleOffer(ctx, session.Customer.ID, string(session.Locale))
	if errors.Is(err, accountcheckout.ErrSimpleUnavailable) {
		return View{}, false
	}
	if err != nil {
		app.logger.Warn("simple subscription lookup failed", "error", err)
		return View{}, false
	}
	return simpleOfferView(session.Locale, offer), true
}

func simpleOfferView(locale Locale, offer accountcheckout.SimpleOffer) View {
	lines := []string{text(locale, "simple.title", html.EscapeString(offer.Plan.Name)), ""}
	if offer.Plan.Description != "" {
		lines = append(lines, html.EscapeString(offer.Plan.Description), "")
	}
	if offer.PeriodDays > 0 {
		lines = append(lines, text(locale, "simple.period", dayCount(locale, offer.PeriodDays)))
	} else {
		lines = append(lines, text(locale, "simple.period", formatDuration(locale, offer.Plan.Duration)))
	}
	switch {
	case offer.TrafficGB > 0:
		lines = append(lines, text(locale, "simple.traffic", text(locale, "traffic.gb", offer.TrafficGB)))
	case offer.Plan.TrafficAllowanceBytes != nil:
		lines = append(lines, text(locale, "simple.traffic", formatBytes(*offer.Plan.TrafficAllowanceBytes)))
	default:
		lines = append(lines, text(locale, "simple.traffic", text(locale, "traffic.unlimited")))
	}
	devices := offer.DeviceQuantity * offer.DeviceSlots
	if offer.Plan.DeviceLimit != nil {
		lines = append(lines, text(locale, "simple.devices", int(*offer.Plan.DeviceLimit)+devices))
	}
	lines = append(lines, "", text(locale, "simple.price", formatMoney(offer.TotalMinor, offer.Currency)))
	return View{
		Text: strings.Join(lines, "\n"),
		Keyboard: keyboard(
			row(actionButton(text(locale, "plan.buy"), "simple-buy")),
			row(callbackButton(text(locale, "action.menu"), routeHome)),
		),
	}
}

// buySimple opens the preconfigured checkout and asks for the payment
// method.
func (app *App) buySimple(ctx context.Context, session commerceContext) View {
	if view, blocked := app.blockedByMaintenance(ctx, session); blocked {
		return view
	}
	view, err := app.commerce.checkout.OpenSimple(ctx, session.Customer.ID, string(session.Locale))
	if err != nil {
		app.logger.Warn("simple checkout could not be opened", "error", err)
		return View{Text: text(session.Locale, "error.order"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.plans"), routePlans)))}
	}
	plan, err := app.customers.Plan(ctx, view.PlanVersionID, session.Locale, app.settings.Currency)
	if err != nil {
		app.logError(ctx, "plan lookup failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	choices, err := app.commerce.PaymentChoices(ctx, view.PlanVersionID)
	if err == nil {
		choices, err = accountcheckout.LoyaltyChoices(ctx, app.commerce.orders, session.Customer.ID, choices)
	}
	if err != nil {
		app.logError(ctx, "payment method lookup failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	return paymentMethodView(session.Locale, plan, choices, "simple")
}
