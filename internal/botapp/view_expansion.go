package botapp

import (
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// topUpView offers the operator's preset amounts plus free entry. Only presets
// that would actually be accepted are rendered, so the customer never taps a
// button the backend then refuses.
func topUpView(locale Locale, limits commerce.TopUpLimits, currency string, balanceMinor, creditedInWindow int64) View {
	if !limits.Enabled {
		return View{
			Text:     text(locale, "topup.disabled"),
			Keyboard: keyboard(row(callbackButton(text(locale, "menu.wallet"), routeWallet))),
		}
	}
	body := &strings.Builder{}
	body.WriteString(text(locale, "topup.title", formatMoney(balanceMinor, currency)))
	body.WriteString("\n\n")
	body.WriteString(text(locale, "topup.bounds", formatMoney(limits.Minimum(), currency), formatMoney(limits.MaximumMinor, currency)))
	if limits.WindowLimitMinor > 0 && limits.Window > 0 {
		remaining := max(limits.WindowLimitMinor-creditedInWindow, 0)
		body.WriteString("\n")
		body.WriteString(text(locale, "topup.window", formatMoney(remaining, currency), formatHours(locale, limits.Window)))
	}
	presets := limits.OfferedPresets(creditedInWindow)
	buttons := make([][]models.InlineKeyboardButton, 0, len(presets)/2+3)
	current := make([]models.InlineKeyboardButton, 0, 2)
	for _, preset := range presets {
		current = append(current, actionButton(formatMoney(preset, currency), "topup:"+strconv.FormatInt(preset, 10)))
		if len(current) == 2 {
			buttons = append(buttons, current)
			current = make([]models.InlineKeyboardButton, 0, 2)
		}
	}
	if len(current) > 0 {
		buttons = append(buttons, current)
	}
	if len(presets) == 0 {
		body.WriteString("\n\n")
		body.WriteString(text(locale, "topup.noPresets"))
	}
	buttons = append(buttons,
		row(actionButton(text(locale, "topup.custom"), "topup-custom")),
		row(actionButton(text(locale, "topup.history"), "topup-history")),
		row(callbackButton(text(locale, "menu.wallet"), routeWallet)),
	)
	return View{Text: body.String(), Keyboard: keyboard(buttons...), RetryRoute: routeTopUp}
}

// topUpMethodView asks which configured adapter settles a top-up.
func topUpMethodView(locale Locale, amountMinor int64, currency string, choices []PaymentChoice) View {
	if len(choices) == 0 {
		return View{Text: text(locale, "pay.none"), Keyboard: keyboard(row(callbackButton(text(locale, "menu.wallet"), routeWallet)))}
	}
	buttons := make([][]models.InlineKeyboardButton, 0, len(choices)+1)
	for _, choice := range choices {
		label := text(locale, "pay."+choice.Provider)
		buttons = append(buttons, row(actionButton(label, "topup-pm:"+choice.Provider+":"+strconv.FormatInt(amountMinor, 10))))
	}
	buttons = append(buttons, row(callbackButton(text(locale, "action.back"), routeTopUp)))
	return View{Text: text(locale, "topup.method", formatMoney(amountMinor, currency)), Keyboard: keyboard(buttons...)}
}

// topUpHistoryView lists past top-ups with the state each one reached.
func topUpHistoryView(locale Locale, rows []dbgen.ListWalletTopupsRow) View {
	if len(rows) == 0 {
		return View{Text: text(locale, "topup.historyEmpty"), Keyboard: keyboard(row(callbackButton(text(locale, "action.back"), routeTopUp)))}
	}
	body := &strings.Builder{}
	body.WriteString(text(locale, "topup.historyTitle"))
	body.WriteString("\n")
	buttons := make([][]models.InlineKeyboardButton, 0, len(rows)+1)
	for _, entry := range rows {
		amount := entry.RequestedMinor
		state := entry.State
		if entry.CreditedAt.Valid {
			amount, state = entry.CreditedMinor, "credited"
		}
		fmt.Fprintf(body, "\n• %s · %s · %s", formatMoney(amount, entry.Currency),
			html.EscapeString(text(locale, "topup.state."+state)), formatDate(entry.CreatedAt.Time))
		if !entry.CreditedAt.Valid && entry.State == "pending" {
			buttons = append(buttons, row(actionButton(text(locale, "topup.openPending"), "order:"+uuidText(entry.OrderID))))
		}
	}
	buttons = append(buttons, row(callbackButton(text(locale, "action.back"), routeTopUp)))
	return View{Text: body.String(), Keyboard: keyboard(buttons...)}
}

// cartView explains a saved cart, what it still needs, and what happens next.
func cartView(locale Locale, cart commercepg.Cart, quote commerce.CartQuote, planName string, now time.Time) View {
	body := &strings.Builder{}
	body.WriteString(text(locale, "cart.title"))
	body.WriteString("\n\n")
	body.WriteString(text(locale, "cart.plan", html.EscapeString(planName)))
	body.WriteString("\n")
	body.WriteString(text(locale, "cart.total", formatMoney(quote.TotalMinor, quote.Currency)))
	body.WriteString("\n")
	body.WriteString(text(locale, "cart.balance", formatMoney(quote.WalletBalanceMinor, quote.Currency)))
	if quote.AddonMinor > 0 {
		body.WriteString("\n")
		body.WriteString(text(locale, "cart.addons", formatMoney(quote.AddonMinor, quote.Currency)))
	}
	if quote.PromoRejection != "" {
		body.WriteString("\n\n")
		body.WriteString(text(locale, "promo.rejected", text(locale, "promo."+quote.PromoRejection)))
	}
	body.WriteString("\n\n")
	switch {
	case quote.Covered() && cart.AutoPurchase:
		body.WriteString(text(locale, "cart.readyAuto"))
	case quote.Covered():
		body.WriteString(text(locale, "cart.readyManual"))
	case cart.AutoPurchase:
		body.WriteString(text(locale, "cart.waiting", formatMoney(quote.MissingMinor(), quote.Currency)))
	default:
		body.WriteString(text(locale, "cart.waitingManual", formatMoney(quote.MissingMinor(), quote.Currency)))
	}
	if remaining := cartExpiry(cart, now); remaining > 0 {
		body.WriteString("\n")
		body.WriteString(text(locale, "cart.expires", formatDate(cart.ExpiresAt.Time)))
	}
	buttons := make([][]models.InlineKeyboardButton, 0, 5)
	if quote.Covered() {
		buttons = append(buttons, row(actionButton(text(locale, "cart.buyNow"), "cart-buy")))
	} else {
		buttons = append(buttons, row(callbackButton(text(locale, "cart.topUp"), routeTopUp)))
	}
	autoLabel := text(locale, "cart.autoOff")
	autoTarget := "cart-auto:on"
	if cart.AutoPurchase {
		autoLabel, autoTarget = text(locale, "cart.autoOn"), "cart-auto:off"
	}
	buttons = append(buttons,
		row(actionButton(autoLabel, autoTarget)),
		row(actionButton(text(locale, "cart.clear"), "cart-clear")),
		row(callbackButton(text(locale, "action.menu"), routeHome)),
	)
	return View{Text: body.String(), Keyboard: keyboard(buttons...), RetryRoute: routeCart}
}

func cartEmptyView(locale Locale) View {
	return View{
		Text:       text(locale, "cart.empty"),
		Keyboard:   keyboard(row(callbackButton(text(locale, "menu.plans"), routePlans)), row(callbackButton(text(locale, "action.menu"), routeHome))),
		RetryRoute: routeCart,
	}
}

// subscriptionsView is the switcher. A single-subscription installation never
// reaches it: the caller renders the subscription screen directly instead.
func subscriptionsView(locale Locale, subscriptions []SubscriptionSummary, canAdd bool, now time.Time) View {
	body := &strings.Builder{}
	body.WriteString(text(locale, "subs.title"))
	buttons := make([][]models.InlineKeyboardButton, 0, len(subscriptions)+2)
	for _, subscription := range subscriptions {
		phase := subscription.Phase(now, 0, 0)
		fmt.Fprintf(body, "\n\n<b>%s</b>\n%s", html.EscapeString(subscription.Label), phaseLabel(locale, phase))
		if subscription.Found && !subscription.EndsAt.IsZero() {
			fmt.Fprintf(body, "\n%s", text(locale, "subs.until", formatDate(subscription.EndsAt)))
		}
		buttons = append(buttons, row(actionButton(subscription.Label, "sub:"+subscription.ID)))
	}
	if len(subscriptions) == 0 {
		body.WriteString("\n\n")
		body.WriteString(text(locale, "subs.empty"))
	}
	if canAdd {
		buttons = append(buttons, row(actionButton(text(locale, "subs.add"), "sub-new")))
	}
	buttons = append(buttons, row(callbackButton(text(locale, "action.menu"), routeHome)))
	return View{Text: body.String(), Keyboard: keyboard(buttons...), RetryRoute: routeSubscriptions}
}

// subscriptionDetailView names the subscription unambiguously in every state, so
// an action can never be applied to the wrong one by accident.
func subscriptionDetailView(locale Locale, subscription SubscriptionSummary, now time.Time, hasAddons bool) View {
	body := &strings.Builder{}
	fmt.Fprintf(body, "%s\n\n", text(locale, "subs.detail", html.EscapeString(subscription.Label)))
	if subscription.Found {
		body.WriteString(text(locale, "subs.plan", html.EscapeString(subscription.PlanName)))
		body.WriteString("\n")
		body.WriteString(phaseLabel(locale, subscription.Phase(now, 0, 0)))
		if !subscription.EndsAt.IsZero() {
			body.WriteString("\n")
			body.WriteString(text(locale, "subs.until", formatDate(subscription.EndsAt)))
		}
	} else {
		body.WriteString(text(locale, "life.none"))
	}
	buttons := make([][]models.InlineKeyboardButton, 0, 7)
	if subscription.Provisioned() {
		buttons = append(buttons,
			row(actionButton(text(locale, "connect.title.short"), "sub-connect:"+subscription.ID)),
			row(actionButton(text(locale, "subs.devices"), "sub-devices:"+subscription.ID)),
			row(actionButton(text(locale, "subs.rotate"), "sub-revoke-confirm:"+subscription.ID)),
		)
	}
	if subscription.Found {
		buttons = append(buttons, row(actionButton(text(locale, "menu.renew"), "sub-renew:"+subscription.ID)))
	}
	if hasAddons {
		buttons = append(buttons, row(actionButton(text(locale, "addon.title.short"), "addons:"+subscription.ID)))
	}
	if subscription.Found {
		// Auto-renew is configured per subscription, and this screen is the one
		// place that already names which subscription is meant.
		buttons = append(buttons, row(actionButton(text(locale, "renew.autoRenew"), "renew-settings:"+subscription.ID)))
	}
	buttons = append(buttons,
		row(actionButton(text(locale, "subs.rename"), "sub-rename:"+subscription.ID)),
		row(callbackButton(text(locale, "subs.back"), routeSubscriptions)),
	)
	return View{Text: body.String(), Keyboard: keyboard(buttons...)}
}

// squadConfiguratorView lets a customer choose the optional squads a plan
// exposes. A plan whose squads are assigned automatically never renders it.
func squadConfiguratorView(locale Locale, policy PlanSquadPolicy, selected []string) View {
	chosen := make(map[string]struct{}, len(selected))
	for _, squad := range selected {
		chosen[squad] = struct{}{}
	}
	body := &strings.Builder{}
	body.WriteString(text(locale, "squad.title"))
	body.WriteString("\n\n")
	switch {
	case policy.Selection == "required" && policy.Maximum != nil:
		body.WriteString(text(locale, "squad.required", max(policy.Minimum, 1), *policy.Maximum))
	case policy.Selection == "required":
		body.WriteString(text(locale, "squad.requiredMin", max(policy.Minimum, 1)))
	case policy.Maximum != nil:
		body.WriteString(text(locale, "squad.optionalMax", *policy.Maximum))
	default:
		body.WriteString(text(locale, "squad.optional"))
	}
	buttons := make([][]models.InlineKeyboardButton, 0, len(policy.Offered)+2)
	for _, squad := range policy.Offered {
		mark := "☑️"
		if _, ok := chosen[squad.SquadID]; ok {
			mark = "✅"
		}
		buttons = append(buttons, row(actionButton(mark+" "+squad.Label, "squad:"+squad.SquadID)))
	}
	buttons = append(buttons,
		row(actionButton(text(locale, "action.continue"), "methods")),
		row(callbackButton(text(locale, "menu.plans"), routePlans)),
	)
	return View{Text: body.String(), Keyboard: keyboard(buttons...)}
}

// checkoutAddonView lets a customer add capacity to the subscription they are
// about to buy. It is charged on the same order, so one confirmation is one
// payment.
func checkoutAddonView(locale Locale, addons []Addon, selected []CheckoutAddon) View {
	if len(addons) == 0 {
		return View{Text: text(locale, "addon.empty"), Keyboard: keyboard(row(actionButton(text(locale, "action.back"), "checkout")))}
	}
	chosen := make(map[string]struct{}, len(selected))
	for _, addon := range selected {
		chosen[addon.AddonVersionID] = struct{}{}
	}
	body := &strings.Builder{}
	body.WriteString(text(locale, "addon.title"))
	body.WriteString("\n\n")
	body.WriteString(text(locale, "addon.checkoutHint"))
	buttons := make([][]models.InlineKeyboardButton, 0, len(addons)+1)
	for _, addon := range addons {
		mark := "☑️"
		if _, ok := chosen[addon.AddonVersionID]; ok {
			mark = "✅"
		}
		fmt.Fprintf(body, "\n\n<b>%s</b> — %s\n%s", html.EscapeString(addon.Name),
			formatMoney(addon.AmountMinor, addon.Currency), html.EscapeString(addon.Description))
		buttons = append(buttons, row(actionButton(mark+" "+addon.Name+" · "+formatMoney(addon.AmountMinor, addon.Currency), "addon-toggle:"+addon.AddonVersionID)))
	}
	buttons = append(buttons, row(actionButton(text(locale, "action.continue"), "checkout")))
	return View{Text: body.String(), Keyboard: keyboard(buttons...)}
}

// addonListView offers mid-period add-ons for one subscription and states the
// proration rule for each, so the price is never a surprise.
func addonListView(locale Locale, subscription SubscriptionSummary, addons []Addon) View {
	if len(addons) == 0 {
		return View{Text: text(locale, "addon.empty"), Keyboard: keyboard(row(actionButton(text(locale, "action.back"), "sub:"+subscription.ID)))}
	}
	body := &strings.Builder{}
	fmt.Fprintf(body, "%s\n\n%s\n", text(locale, "addon.title"), text(locale, "subs.detail", html.EscapeString(subscription.Label)))
	buttons := make([][]models.InlineKeyboardButton, 0, len(addons)+1)
	for _, addon := range addons {
		fmt.Fprintf(body, "\n<b>%s</b> — %s\n%s\n%s", html.EscapeString(addon.Name),
			formatMoney(addon.AmountMinor, addon.Currency), html.EscapeString(addon.Description),
			text(locale, "addon.proration."+string(addon.Proration)))
		// The subscription travels as its slot number: Telegram caps callback
		// data at 64 bytes, and an add-on version UUID beside a subscription
		// UUID does not fit.
		buttons = append(buttons, row(actionButton(addon.Name+" · "+formatMoney(addon.AmountMinor, addon.Currency), addonCallback("addon-buy", addon.AddonVersionID, subscription.Slot))))
	}
	buttons = append(buttons, row(actionButton(text(locale, "action.back"), "sub:"+subscription.ID)))
	return View{Text: body.String(), Keyboard: keyboard(buttons...)}
}

// addonCallback builds an add-on action for one subscription slot.
func addonCallback(action, addonVersionID string, slot int) string {
	return action + ":" + addonVersionID + ":" + strconv.Itoa(slot)
}

// addonConfirmView states what a mid-period add-on will cost before it is
// charged. The wallet settles an add-on it can cover the moment the order is
// created, so the amount, the proration rule, and where the money comes from
// are all shown before the one tap that spends it.
func addonConfirmView(locale Locale, subscription SubscriptionSummary, addon Addon, charge commerce.AddonCharge, walletBalanceMinor int64, slot string) View {
	body := &strings.Builder{}
	body.WriteString(text(locale, "addon.confirmTitle"))
	body.WriteString("\n\n")
	body.WriteString(text(locale, "subs.detail", html.EscapeString(subscription.Label)))
	body.WriteString("\n")
	fmt.Fprintf(body, "<b>%s</b>\n%s", html.EscapeString(addon.Name), html.EscapeString(addon.Description))
	body.WriteString("\n\n")
	body.WriteString(text(locale, "addon.proration."+string(addon.Proration)))
	body.WriteString("\n")
	body.WriteString(text(locale, "addon.charge", formatMoney(charge.ChargedMinor, addon.Currency)))
	body.WriteString("\n")
	switch {
	case walletBalanceMinor >= charge.ChargedMinor:
		body.WriteString(text(locale, "addon.walletCovers", formatMoney(walletBalanceMinor, addon.Currency)))
	case walletBalanceMinor > 0:
		body.WriteString(text(locale, "addon.walletPartial", formatMoney(walletBalanceMinor, addon.Currency), formatMoney(charge.ChargedMinor-walletBalanceMinor, addon.Currency)))
	default:
		body.WriteString(text(locale, "addon.walletNone"))
	}
	return View{Text: body.String(), Keyboard: keyboard(
		row(actionButton(text(locale, "addon.confirm"), addonCallback("addon-confirm", addon.AddonVersionID, subscription.Slot))),
		row(actionButton(text(locale, "action.cancel"), "addons:"+subscription.ID)),
	)}
}

// maintenanceView is what a customer sees while purchases are paused. The
// operator's own wording wins when they supplied it.
func maintenanceView(locale Locale, state commerce.Maintenance) View {
	notice := state.NoticeEN
	if locale == LocaleRussian {
		notice = state.NoticeRU
	}
	body := &strings.Builder{}
	body.WriteString(text(locale, "maintenance.title"))
	body.WriteString("\n\n")
	if strings.TrimSpace(notice) != "" {
		body.WriteString(html.EscapeString(notice))
	} else {
		body.WriteString(text(locale, "maintenance.body"))
	}
	if !state.ExpectedReturnAt.IsZero() {
		body.WriteString("\n\n")
		body.WriteString(text(locale, "maintenance.until", formatDate(state.ExpectedReturnAt)))
	}
	return View{
		Text:     body.String(),
		Keyboard: keyboard(row(callbackButton(text(locale, "action.retry"), routeHome)), row(callbackButton(text(locale, "menu.support"), routeSupport))),
	}
}

// phaseLabel renders a subscription phase as one customer-facing line.
func phaseLabel(locale Locale, phase commerce.SubscriptionPhase) string {
	return text(locale, "phase."+string(phase))
}

// formatHours renders a rolling window in whole hours or days.
func formatHours(locale Locale, window time.Duration) string {
	hours := int(window.Hours())
	if hours >= 24 && hours%24 == 0 {
		return plural(locale, hours/24, phrase{ru: "%d день", en: "%d day"}, phrase{ru: "%d дня", en: "%d days"}, phrase{ru: "%d дней", en: "%d days"})
	}
	return plural(locale, max(hours, 1), phrase{ru: "%d час", en: "%d hour"}, phrase{ru: "%d часа", en: "%d hours"}, phrase{ru: "%d часов", en: "%d hours"})
}
