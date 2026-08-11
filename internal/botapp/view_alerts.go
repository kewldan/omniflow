package botapp

import (
	"html"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/commerce"
)

// subscriptionPrefix names the subscription an alert is about. It is empty for a
// customer who holds exactly one, so a single-subscription installation reads
// exactly as it did before.
func subscriptionPrefix(locale Locale, label string) string {
	if label == "" {
		return ""
	}
	return text(locale, "alert.subscription", html.EscapeString(label)) + "\n\n"
}

// expiryAlertView is the v0.2 expiry reminder, now routed through the shared
// delivery policy and named per subscription.
func expiryAlertView(locale Locale, days int, subscriptionLabel string) View {
	body := subscriptionPrefix(locale, subscriptionLabel) + text(locale, "alert.expiry", days)
	return View{Text: body, Keyboard: keyboard(
		row(callbackButton(text(locale, "menu.renew"), routePlans)),
		row(callbackButton(text(locale, "action.menu"), routeHome)),
	)}
}

// trafficAlertView warns that the traffic allowance is running out.
func trafficAlertView(locale Locale, percent int64, subscriptionLabel string) View {
	body := subscriptionPrefix(locale, subscriptionLabel) + text(locale, "alert.traffic", percent)
	return View{Text: body, Keyboard: keyboard(
		row(callbackButton(text(locale, "menu.upgrade"), routePlans)),
		row(callbackButton(text(locale, "action.menu"), routeHome)),
	)}
}

// renewalReminderView offers a direct, idempotent renewal checkout. Tapping it
// twice resumes the same checkout rather than creating a second order, and the
// action names the subscription so a multi-subscription customer renews the one
// the reminder is about.
func renewalReminderView(locale Locale, entitlement Entitlement, days int) View {
	rows := []([]models.InlineKeyboardButton){
		row(actionButton(text(locale, "menu.renew"), renewAction(entitlement))),
		row(callbackButton(text(locale, "menu.plans"), routePlans)),
		row(callbackButton(text(locale, "action.menu"), routeHome)),
	}
	return View{
		Text: subscriptionPrefix(locale, entitlement.SubscriptionLabel) +
			text(locale, "alert.renewal", html.EscapeString(entitlement.PlanName), days, formatDate(entitlement.EndsAt)),
		Keyboard: keyboard(rows...),
	}
}

// renewAction targets the renewal at the subscription the entitlement belongs
// to. Without a subscription it falls back to the plan-only action, which the
// checkout then resolves to the customer's primary subscription.
func renewAction(entitlement Entitlement) string {
	if entitlement.SubscriptionID != "" {
		return "sub-renew:" + entitlement.SubscriptionID
	}
	return "buy:" + entitlement.PlanVersionID + ":extension"
}

// gracePeriodView explains that access continues briefly after expiry.
func gracePeriodView(locale Locale, entitlement Entitlement) View {
	return View{
		Text: subscriptionPrefix(locale, entitlement.SubscriptionLabel) + lifecycleNotice(locale, commerce.PhaseGrace, entitlement),
		Keyboard: keyboard(
			row(actionButton(text(locale, "menu.renew"), renewAction(entitlement))),
			row(callbackButton(text(locale, "action.menu"), routeHome)),
		),
	}
}

// recoveryView offers one-tap recovery for an expired subscription.
func recoveryView(locale Locale, entitlement Entitlement) View {
	return View{
		Text: subscriptionPrefix(locale, entitlement.SubscriptionLabel) + lifecycleNotice(locale, commerce.PhaseExpired, entitlement),
		Keyboard: keyboard(
			row(actionButton(text(locale, "life.recover"), "buy:"+entitlement.PlanVersionID+":purchase")),
			row(callbackButton(text(locale, "menu.plans"), routePlans)),
			row(callbackButton(text(locale, "action.menu"), routeHome)),
		),
	}
}

// fulfillmentAlertView tells a customer that provisioning finished or needs
// attention, without exposing any Remnawave internals.
func fulfillmentAlertView(locale Locale, succeeded bool, subscriptionLabel string) View {
	key := "alert.fulfillmentFailed"
	if succeeded {
		key = "alert.fulfillmentDone"
	}
	return View{Text: subscriptionPrefix(locale, subscriptionLabel) + text(locale, key), Keyboard: keyboard(
		row(callbackButton(text(locale, "connect.title.short"), routeConnect)),
		row(callbackButton(text(locale, "action.menu"), routeHome)),
	)}
}

// dunningAlertView tells a customer that an automatic charge did not go
// through.
//
// The failure code is deliberately absent. "insufficient_funds" and
// "declined" are operator vocabulary; what the customer needs is whether their
// access is still there and what to do, and both messages say exactly that.
// The precise code stays on the attempt row, where support can read it.
func dunningAlertView(locale Locale, abandoned bool) View {
	key := "alert.dunningRetry"
	if abandoned {
		key = "alert.dunningAbandoned"
	}
	return View{Text: text(locale, key), Keyboard: keyboard(
		row(callbackButton(text(locale, "menu.renew"), routePlans)),
		row(callbackButton(text(locale, "menu.settings"), routeSettings)),
		row(callbackButton(text(locale, "action.menu"), routeHome)),
	)}
}
