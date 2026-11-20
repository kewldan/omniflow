package botapp

import (
	"html"
	"math"
	"strconv"
	"time"
)

// planOptionPhrases are the bot's wording for the plan and subscription
// options. They join the shared catalog at start-up, kept in their own file
// so the catalog itself is not edited by every feature.
var planOptionPhrases = map[string]phrase{
	"trial.sign_in_method": {
		ru: "Пробный период недоступен для этого способа входа.",
		en: "The trial is not available for the way this account signs in.",
	},
	"trial.ending": {
		ru: "⏳ Пробный период «%s» заканчивается через %s. Выберите тариф, чтобы не потерять доступ.",
		en: "⏳ Your trial of “%s” ends in %s. Pick a plan to keep your access.",
	},
	"trial.hours": {ru: "%s ч", en: "%s h"},
	"squad.unavailable": {
		ru: "Один из серверов этого тарифа сейчас недоступен, поэтому покупка не оформлена и деньги не списаны. Напишите в поддержку.",
		en: "One of this plan's servers is not available right now, so nothing was bought or charged. Please contact support.",
	},
	"addon.limit": {
		ru: "Это дополнение превысит лимит подписки: устройств или докупленного трафика.",
		en: "That add-on would go past this subscription's limit on devices or bought traffic.",
	},
}

func init() {
	for key, value := range planOptionPhrases {
		catalog[key] = value
	}
}

// trialEndingView warns that a trial ends within the installation's window,
// with the plans one tap away.
func trialEndingView(locale Locale, entitlement Entitlement, now time.Time) View {
	hours := int(math.Ceil(entitlement.EndsAt.Sub(now).Hours()))
	if hours < 1 {
		hours = 1
	}
	body := subscriptionPrefix(locale, entitlement.SubscriptionLabel) +
		text(locale, "trial.ending", html.EscapeString(entitlement.PlanName),
			text(locale, "trial.hours", strconv.Itoa(hours)))
	return View{Text: body, Keyboard: keyboard(
		row(callbackButton(text(locale, "menu.plans"), routePlans)),
		row(callbackButton(text(locale, "action.menu"), routeHome)),
	)}
}
