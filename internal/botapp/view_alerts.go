package botapp

import (
	"html"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/commerce"
)

// expiryAlertView is the v0.2 expiry reminder, now routed through the shared
// delivery policy.
func expiryAlertView(locale Locale, days int) View {
	body := text(locale, "alert.expiry", days)
	return View{Text: body, Keyboard: keyboard(
		row(callbackButton(text(locale, "menu.renew"), routePlans)),
		row(callbackButton(text(locale, "action.menu"), routeHome)),
	)}
}

// trafficAlertView warns that the traffic allowance is running out.
func trafficAlertView(locale Locale, percent int64) View {
	return View{Text: text(locale, "alert.traffic", percent), Keyboard: keyboard(
		row(callbackButton(text(locale, "menu.upgrade"), routePlans)),
		row(callbackButton(text(locale, "action.menu"), routeHome)),
	)}
}

// renewalReminderView offers a direct, idempotent renewal checkout. Tapping it
// twice resumes the same checkout rather than creating a second order.
func renewalReminderView(locale Locale, entitlement Entitlement, days int) View {
	rows := []([]models.InlineKeyboardButton){
		row(actionButton(text(locale, "menu.renew"), "buy:"+entitlement.PlanVersionID+":extension")),
		row(callbackButton(text(locale, "menu.plans"), routePlans)),
		row(callbackButton(text(locale, "action.menu"), routeHome)),
	}
	return View{
		Text:     text(locale, "alert.renewal", html.EscapeString(entitlement.PlanName), days, formatDate(entitlement.EndsAt)),
		Keyboard: keyboard(rows...),
	}
}

// gracePeriodView explains that access continues briefly after expiry.
func gracePeriodView(locale Locale, entitlement Entitlement) View {
	return View{
		Text: lifecycleNotice(locale, commerce.PhaseGrace, entitlement),
		Keyboard: keyboard(
			row(actionButton(text(locale, "menu.renew"), "buy:"+entitlement.PlanVersionID+":extension")),
			row(callbackButton(text(locale, "action.menu"), routeHome)),
		),
	}
}

// recoveryView offers one-tap recovery for an expired subscription.
func recoveryView(locale Locale, entitlement Entitlement) View {
	return View{
		Text: lifecycleNotice(locale, commerce.PhaseExpired, entitlement),
		Keyboard: keyboard(
			row(actionButton(text(locale, "life.recover"), "buy:"+entitlement.PlanVersionID+":purchase")),
			row(callbackButton(text(locale, "menu.plans"), routePlans)),
			row(callbackButton(text(locale, "action.menu"), routeHome)),
		),
	}
}

// fulfillmentAlertView tells a customer that provisioning finished or needs
// attention, without exposing any Remnawave internals.
func fulfillmentAlertView(locale Locale, succeeded bool) View {
	key := "alert.fulfillmentFailed"
	if succeeded {
		key = "alert.fulfillmentDone"
	}
	return View{Text: text(locale, key), Keyboard: keyboard(
		row(callbackButton(text(locale, "connect.title.short"), routeConnect)),
		row(callbackButton(text(locale, "action.menu"), routeHome)),
	)}
}
