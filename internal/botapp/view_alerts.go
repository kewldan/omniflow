package botapp

import (
	"html"
	"strconv"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/notice"
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
// delivery policy, named per subscription, and worded by the operator if they
// have said otherwise.
func expiryAlertView(set notices, locale Locale, days int, subscriptionLabel string) View {
	body := subscriptionPrefix(locale, subscriptionLabel) +
		set.text(locale, notice.CodeExpiry, map[string]string{"days": strconv.Itoa(days)})
	return View{Text: body, Keyboard: keyboard(
		row(callbackButton(text(locale, "menu.renew"), routePlans)),
		row(callbackButton(text(locale, "action.menu"), routeHome)),
	)}
}

// trafficAlertView warns that the traffic allowance is running out.
func trafficAlertView(set notices, locale Locale, percent int64, subscriptionLabel string) View {
	body := subscriptionPrefix(locale, subscriptionLabel) +
		set.text(locale, notice.CodeTraffic, map[string]string{
			"percent": strconv.FormatInt(percent, 10),
		})
	return View{Text: body, Keyboard: keyboard(
		row(callbackButton(text(locale, "menu.upgrade"), routePlans)),
		row(callbackButton(text(locale, "action.menu"), routeHome)),
	)}
}

// renewalReminderView offers a direct, idempotent renewal checkout. Tapping it
// twice resumes the same checkout rather than creating a second order, and the
// action names the subscription so a multi-subscription customer renews the one
// the reminder is about.
func renewalReminderView(set notices, locale Locale, entitlement Entitlement, days int) View {
	rows := []([]models.InlineKeyboardButton){
		row(actionButton(text(locale, "menu.renew"), renewAction(entitlement))),
		row(callbackButton(text(locale, "menu.plans"), routePlans)),
		row(callbackButton(text(locale, "action.menu"), routeHome)),
	}
	return View{
		// The plan name is escaped because it is operator-entered and the body is
		// parsed as HTML. The wording around it was validated when it was saved;
		// the value substituted into it was not, and never can be.
		Text: subscriptionPrefix(locale, entitlement.SubscriptionLabel) +
			set.text(locale, notice.CodeRenewal, map[string]string{
				"plan":  html.EscapeString(entitlement.PlanName),
				"days":  strconv.Itoa(days),
				"until": formatDate(entitlement.EndsAt),
			}),
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
func gracePeriodView(set notices, locale Locale, entitlement Entitlement) View {
	return View{
		Text: subscriptionPrefix(locale, entitlement.SubscriptionLabel) +
			set.text(locale, notice.CodeGrace, map[string]string{
				"until":      formatDate(entitlement.EndsAt),
				"graceUntil": formatDate(entitlement.EndsAt.Add(entitlement.GracePeriod)),
			}),
		Keyboard: keyboard(
			row(actionButton(text(locale, "menu.renew"), renewAction(entitlement))),
			row(callbackButton(text(locale, "action.menu"), routeHome)),
		),
	}
}

// recoveryView offers one-tap recovery for an expired subscription.
func recoveryView(set notices, locale Locale, entitlement Entitlement) View {
	return View{
		Text: subscriptionPrefix(locale, entitlement.SubscriptionLabel) +
			set.text(locale, notice.CodeRecovery, nil),
		Keyboard: keyboard(
			row(actionButton(text(locale, "life.recover"), "buy:"+entitlement.PlanVersionID+":purchase")),
			row(callbackButton(text(locale, "menu.plans"), routePlans)),
			row(callbackButton(text(locale, "action.menu"), routeHome)),
		),
	}
}

// fulfillmentAlertView tells a customer that provisioning finished or needs
// attention, without exposing any Remnawave internals.
func fulfillmentAlertView(set notices, locale Locale, succeeded bool, subscriptionLabel string) View {
	code := notice.CodeFulfillmentFailed
	if succeeded {
		code = notice.CodeFulfillmentSucceeded
	}
	body := subscriptionPrefix(locale, subscriptionLabel) + set.text(locale, code, nil)
	return View{Text: body, Keyboard: keyboard(
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
func dunningAlertView(set notices, locale Locale, abandoned bool) View {
	code := notice.CodeDunningRetry
	if abandoned {
		code = notice.CodeDunningAbandoned
	}
	return View{Text: set.text(locale, code, nil), Keyboard: keyboard(
		row(callbackButton(text(locale, "menu.renew"), routePlans)),
		row(callbackButton(text(locale, "menu.settings"), routeSettings)),
		row(callbackButton(text(locale, "action.menu"), routeHome)),
	)}
}
