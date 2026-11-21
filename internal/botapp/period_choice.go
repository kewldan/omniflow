package botapp

import (
	"context"
	"html"
	"sort"
	"strconv"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/accountcheckout"
)

// The billing period of a purchase or a renewal in the chat.
//
// A version sells its own period and may sell others — seven days, three
// months, a year — each priced on its own. The web checkout has always let the
// customer pick; the bot sold the version's own period only, which pushed an
// operator with one plan and five periods into five plans, and turned a move
// from a month to three months into a plan change. Now the bot asks, between
// the plan and the payment method, whenever the version sells more than its
// own period for this kind of order, and marks the period the operator
// highlighted.

// periodChoiceRow is one button of the picker: days 0 is the version's own.
type periodChoiceRow struct {
	days        int
	duration    time.Duration
	amountMinor int64
	highlighted bool
}

// periodPickerView asks for the period. chosen is the period already on the
// checkout, -1 for none yet.
func periodPickerView(locale Locale, plan Plan, offer accountcheckout.PeriodOffer, chosen int, back string) View {
	choices := []periodChoiceRow{{days: 0, duration: plan.Duration, amountMinor: plan.AmountMinor}}
	for _, choice := range offer.Choices {
		choices = append(choices, periodChoiceRow{
			days: choice.Days, duration: time.Duration(choice.Days) * 24 * time.Hour,
			amountMinor: choice.AmountMinor, highlighted: choice.Highlighted,
		})
	}
	sort.SliceStable(choices, func(i, j int) bool { return choices[i].duration < choices[j].duration })
	rows := make([][]models.InlineKeyboardButton, 0, len(choices)+1)
	for _, choice := range choices {
		label := text(locale, "period.option", formatDuration(locale, choice.duration), formatMoney(choice.amountMinor, offer.Currency))
		if choice.highlighted {
			label = text(locale, "period.best", label)
		}
		if choice.days == chosen {
			label = "✅ " + label
		}
		rows = append(rows, row(actionButton(label, "period:"+strconv.Itoa(choice.days))))
	}
	rows = append(rows, row(actionButton(text(locale, "action.back"), back)))
	return View{
		Text:     text(locale, "period.title") + "\n\n" + text(locale, "checkout.plan", html.EscapeString(plan.Name)),
		Keyboard: keyboard(rows...),
	}
}

// periodOffer is what the open checkout's version sells beside its own
// period, in the installation currency. A lookup failure offers nothing, so
// the customer still buys the version's own period.
func (app *App) periodOffer(ctx context.Context, checkout CheckoutSession) accountcheckout.PeriodOffer {
	offer, err := app.commerce.PlanPeriods(ctx, checkout.PlanVersionID, app.settings.Currency, checkout.Operation)
	if err != nil {
		app.logger.Warn("plan period lookup failed", "error", err)
		return accountcheckout.PeriodOffer{Currency: app.settings.Currency}
	}
	return offer
}

// periodMenu shows the picker for the open checkout again.
func (app *App) periodMenu(ctx context.Context, session commerceContext) View {
	checkout, found, err := app.customers.Checkout(ctx, session.Customer.ID)
	if err != nil || !found {
		return View{Text: text(session.Locale, "checkout.expired"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.plans"), routePlans)))}
	}
	plan, err := app.customers.Plan(ctx, checkout.PlanVersionID, session.Locale, app.settings.Currency)
	if err != nil {
		return View{Text: text(session.Locale, "plan.gone"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routePlans)))}
	}
	return periodPickerView(session.Locale, plan, app.periodOffer(ctx, checkout), checkout.PeriodDays, "plan:"+plan.PlanVersionID)
}

// choosePeriod records the tapped period and moves on to paying for it.
func (app *App) choosePeriod(ctx context.Context, session commerceContext, argument string) View {
	days, err := strconv.Atoi(argument)
	if err != nil {
		return app.errorView(session.Locale, routePlans)
	}
	checkout, found, err := app.customers.Checkout(ctx, session.Customer.ID)
	if err != nil || !found {
		return View{Text: text(session.Locale, "checkout.expired"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "menu.plans"), routePlans)))}
	}
	plan, err := app.customers.Plan(ctx, checkout.PlanVersionID, session.Locale, app.settings.Currency)
	if err != nil {
		return View{Text: text(session.Locale, "plan.gone"), Keyboard: keyboard(row(callbackButton(text(session.Locale, "action.back"), routePlans)))}
	}
	if checkout, err = app.commerce.ChoosePeriod(ctx, checkout, days); err != nil {
		app.logger.Info("billing period refused", "days", days, "error", err)
		return app.periodMenu(ctx, session)
	}
	return app.checkoutPaymentMethods(ctx, session, checkout, plan, "period-menu")
}

// checkoutPaymentMethods asks for the payment method of an open checkout,
// with each method's price for the period chosen. A plan that lets the
// customer choose squads asks for them first.
func (app *App) checkoutPaymentMethods(ctx context.Context, session commerceContext, checkout CheckoutSession, plan Plan, back string) View {
	if policy, policyErr := app.customers.PlanSquads(ctx, checkout.PlanVersionID, session.Locale); policyErr == nil && policy.Configurable() {
		return squadConfiguratorView(session.Locale, policy, checkout.SelectedSquadIDs)
	}
	choices, err := app.commerce.CheckoutPaymentChoices(ctx, checkout)
	if err == nil {
		choices, err = accountcheckout.LoyaltyChoices(ctx, app.commerce.orders, session.Customer.ID, choices)
	}
	if err != nil {
		app.logger.Error("payment method lookup failed", "error", err)
		return app.errorView(session.Locale, routePlans)
	}
	return paymentMethodView(session.Locale, plan, choices, back)
}

// checkoutDuration is how long the open checkout buys: its chosen period, or
// the version's own.
func checkoutDuration(plan Plan, checkout CheckoutSession) time.Duration {
	if checkout.PeriodDays > 0 {
		return time.Duration(checkout.PeriodDays) * 24 * time.Hour
	}
	return plan.Duration
}
