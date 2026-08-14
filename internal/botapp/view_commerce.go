package botapp

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/commerce"
)

// plansView lists the catalog with the comparison facts a customer needs before
// opening a plan: period, traffic, devices, and price in one line each.
func plansView(locale Locale, plans []Plan, currency string) View {
	if len(plans) == 0 {
		return View{Text: text(locale, "plans.empty", currency), Keyboard: keyboard(row(callbackButton(text(locale, "action.back"), routeHome))), RetryRoute: routePlans}
	}
	lines := make([]string, 0, len(plans)+1)
	lines = append(lines, text(locale, "plans.title"))
	rows := make([][]models.InlineKeyboardButton, 0, len(plans)+1)
	for _, plan := range plans {
		badge := ""
		if plan.Kind == "trial" {
			badge = " " + text(locale, "plans.trialBadge")
		}
		lines = append(lines, fmt.Sprintf("\n<b>%s</b>%s\n%s\n%s\n%s\n%s",
			html.EscapeString(plan.Name), badge,
			text(locale, "plans.duration", formatDuration(locale, plan.Duration)),
			text(locale, "plans.traffic", trafficAllowance(locale, plan.TrafficAllowanceBytes)),
			text(locale, "plans.devices", deviceAllowance(locale, plan.DeviceLimit)),
			text(locale, "plans.price", formatMoney(plan.AmountMinor, plan.Currency))))
		label := text(locale, "plans.row", truncateRunes(plan.Name, 24), formatMoney(plan.AmountMinor, plan.Currency))
		rows = append(rows, row(actionButton(label, "plan:"+plan.PlanVersionID)))
	}
	rows = append(rows, row(callbackButton(text(locale, "action.refresh"), routePlans), callbackButton(text(locale, "action.back"), routeHome)))
	return View{Text: strings.Join(lines, "\n"), Keyboard: keyboard(rows...), RetryRoute: routePlans}
}

// planView shows one plan with its eligibility, policies, and the actions the
// configured policy actually permits.
func planView(locale Locale, plan Plan, entitlement Entitlement, termsURL string, trialReason string) View {
	sections := []string{
		text(locale, "plan.title", html.EscapeString(plan.Name)),
	}
	if description := strings.TrimSpace(plan.Description); description != "" {
		sections = append(sections, html.EscapeString(description))
	}
	sections = append(sections, strings.Join([]string{
		text(locale, "plans.duration", formatDuration(locale, plan.Duration)),
		text(locale, "plans.traffic", trafficAllowance(locale, plan.TrafficAllowanceBytes)),
		text(locale, "plans.devices", deviceAllowance(locale, plan.DeviceLimit)),
		text(locale, "plans.price", formatMoney(plan.AmountMinor, plan.Currency)),
	}, "\n"))
	policies := []string{
		text(locale, "plan.policyUpgrade", policyLabel(locale, plan.UpgradePolicy)),
		text(locale, "plan.policyDowngrade", policyLabel(locale, plan.DowngradePolicy)),
		text(locale, "plan.policyCancellation", policyLabel(locale, plan.CancellationPolicy)),
	}
	if plan.GracePeriod > 0 {
		policies = append(policies, text(locale, "plan.grace", formatDuration(locale, plan.GracePeriod)))
	}
	sections = append(sections, strings.Join(policies, "\n"))
	if trialReason != "" {
		sections = append(sections, text(locale, "trial.rejected", text(locale, "trial."+trialReason)))
	}
	sections = append(sections, text(locale, "plan.terms"))

	rows := make([][]models.InlineKeyboardButton, 0, 5)
	for _, choice := range planActions(locale, plan, entitlement, trialReason) {
		rows = append(rows, row(actionButton(choice.label, "buy:"+plan.PlanVersionID+":"+choice.operation)))
	}
	if safeURL(termsURL) {
		rows = append(rows, row(models.InlineKeyboardButton{Text: text(locale, "action.terms"), URL: termsURL}))
	}
	rows = append(rows, row(callbackButton(text(locale, "action.back"), routePlans)))
	return View{Text: strings.Join(sections, "\n\n"), Keyboard: keyboard(rows...), RetryRoute: routePlans}
}

type planAction struct{ label, operation string }

// planActions decides which purchase paths a plan offers. A policy of "forbid"
// removes the action entirely rather than failing after the customer taps it.
func planActions(locale Locale, plan Plan, entitlement Entitlement, trialReason string) []planAction {
	if plan.Kind == "trial" {
		if trialReason != "" {
			return nil
		}
		return []planAction{{text(locale, "plan.trial"), "purchase"}}
	}
	if !entitlement.Found || entitlement.Status == "expired" || entitlement.EndsAt.Before(time.Now()) {
		return []planAction{{text(locale, "plan.buy"), "purchase"}}
	}
	if entitlement.PlanVersionID == plan.PlanVersionID {
		return []planAction{{text(locale, "plan.renew"), "extension"}}
	}
	actions := make([]planAction, 0, 2)
	if commerce.AllowedOperation("upgrade", plan.UpgradePolicy, plan.DowngradePolicy) {
		actions = append(actions, planAction{text(locale, "plan.upgrade"), "upgrade"})
	}
	if commerce.AllowedOperation("downgrade", plan.UpgradePolicy, plan.DowngradePolicy) {
		actions = append(actions, planAction{text(locale, "plan.downgrade"), "downgrade"})
	}
	return actions
}

// paymentMethodView offers only the adapters that are enabled and can settle a
// currency the plan is priced in.
func paymentMethodView(locale Locale, plan Plan, choices []PaymentChoice) View {
	if len(choices) == 0 {
		return View{Text: text(locale, "pay.none"), Keyboard: keyboard(row(callbackButton(text(locale, "action.back"), routePlans)))}
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(choices)+1)
	for _, choice := range choices {
		label := text(locale, "pay.option", providerLabel(locale, choice.Provider), formatMoney(choice.AmountMinor, choice.Currency))
		rows = append(rows, row(actionButton(label, "pm:"+choice.Provider+":"+choice.Currency)))
	}
	rows = append(rows, row(actionButton(text(locale, "action.back"), "plan:"+plan.PlanVersionID)))
	return View{Text: text(locale, "pay.choose"), Keyboard: keyboard(rows...)}
}

func providerLabel(locale Locale, provider string) string {
	label := text(locale, "pay."+provider)
	if label == "pay."+provider {
		return provider
	}
	return label
}

// checkoutView is the confirmation screen: the exact breakdown the customer is
// agreeing to, plus the promo and wallet controls that change it.
func checkoutView(locale Locale, plan Plan, session CheckoutSession, quote commerce.CheckoutQuote, addonsAvailable bool) View {
	lines := []string{
		text(locale, "checkout.title"),
		"",
		text(locale, "checkout.plan", html.EscapeString(plan.Name)),
		text(locale, "checkout.period", formatDuration(locale, plan.Duration)),
		text(locale, "checkout.method", providerLabel(locale, session.Provider)),
		"",
		text(locale, "checkout.subtotal", formatMoney(quote.Subtotal.Amount, quote.Subtotal.Currency)),
	}
	if quote.DiscountMinor > 0 {
		lines = append(lines, text(locale, "checkout.discount", formatMoney(quote.DiscountMinor, quote.Subtotal.Currency)))
	}
	if quote.AddonMinor > 0 {
		lines = append(lines, text(locale, "checkout.addons", formatMoney(quote.AddonMinor, quote.Subtotal.Currency)))
	}
	if quote.WalletAppliedMinor > 0 || quote.WalletBalanceMinor > 0 {
		lines = append(lines, text(locale, "checkout.wallet", formatMoney(quote.WalletAppliedMinor, quote.Subtotal.Currency), formatMoney(quote.WalletBalanceMinor, quote.Subtotal.Currency)))
	}
	if quote.ExternalMinor == 0 {
		lines = append(lines, text(locale, "checkout.free"))
	} else {
		lines = append(lines, text(locale, "checkout.total", formatMoney(quote.ExternalMinor, quote.Subtotal.Currency)))
	}
	if quote.PromoRejection != "" {
		lines = append(lines, "", text(locale, "promo.rejected", text(locale, "promo."+quote.PromoRejection)))
	}
	lines = append(lines, text(locale, "plan.terms"))

	promoRow := row(actionButton(text(locale, "checkout.promoAdd"), "promo"))
	if quote.PromoCode != "" {
		promoRow = row(actionButton(text(locale, "checkout.promoOn", quote.PromoCode), "promo"), actionButton(text(locale, "checkout.promoOff"), "promo-clear"))
	}
	walletLabel := text(locale, "checkout.walletOff")
	if session.ApplyWallet {
		walletLabel = text(locale, "checkout.walletOn")
	}
	rows := [][]models.InlineKeyboardButton{
		row(actionButton(text(locale, "checkout.pay"), "confirm")),
		promoRow,
	}
	if quote.WalletBalanceMinor > 0 {
		rows = append(rows, row(actionButton(walletLabel, "wallet-toggle")))
	}
	if addonsAvailable {
		rows = append(rows, row(actionButton(text(locale, "addon.title.short"), "checkout-addons")))
	}
	// An order the wallet cannot cover can be saved instead of abandoned: the
	// customer tops up and it is bought automatically once the balance is there.
	if quote.ExternalMinor > 0 {
		rows = append(rows, row(actionButton(text(locale, "cart.save"), "cart-save")))
	}
	rows = append(rows,
		row(actionButton(text(locale, "checkout.changeMethod"), "buy:"+session.PlanVersionID+":"+session.Operation)),
		row(callbackButton(text(locale, "action.cancel"), routePlans)),
	)
	return View{Text: strings.Join(lines, "\n"), Keyboard: keyboard(rows...), Protect: true}
}

// promoPromptView asks for a promo code and always offers a way out.
func promoPromptView(locale Locale) View {
	return View{Text: text(locale, "promo.prompt"), Keyboard: keyboard(row(actionButton(text(locale, "action.cancel"), "checkout")))}
}

// orderStatusView renders every terminal and intermediate checkout state:
// pending, succeeded, provisioning, completed, failed, cancelled, expired, and
// refunded. Refreshing is always available and never loses the order.
func orderStatusView(locale Locale, order OrderSummary, refunds []RefundStatus) View {
	rows := make([][]models.InlineKeyboardButton, 0, 4)
	var body string
	switch order.Phase {
	case commerce.PaymentPhaseAwaitingAction, commerce.PaymentPhasePending:
		body = text(locale, "payment.pending", shortID(order.ID), formatMoney(order.ExternalMinor, order.Currency))
		if !order.ExpiresAt.IsZero() {
			body += text(locale, "payment.expires", formatDate(order.ExpiresAt))
		}
		if order.Provider == "manual" {
			body += text(locale, "payment.manual")
		}
		body += text(locale, "payment.delayed")
		if safeURL(order.CheckoutURL) {
			rows = append(rows, row(models.InlineKeyboardButton{Text: text(locale, "payment.open"), URL: order.CheckoutURL}))
		}
		if order.Provider == "telegram_stars" {
			rows = append(rows, row(actionButton(text(locale, "payment.invoice"), "invoice:"+order.ID)))
		}
		rows = append(rows, row(actionButton(text(locale, "orders.cancel"), "order-cancel:"+order.ID)))
	case commerce.PaymentPhaseSucceeded:
		body = text(locale, "payment.succeeded", shortID(order.ID))
	case commerce.PaymentPhaseProvisioning:
		body = text(locale, "payment.provisioning")
	case commerce.PaymentPhaseCompleted:
		body = text(locale, "payment.completed")
		rows = append(rows, row(callbackButton(text(locale, "connect.title.short"), routeConnect)))
	case commerce.PaymentPhaseFailed:
		body = text(locale, "payment.failed")
		rows = append(rows, row(callbackButton(text(locale, "menu.plans"), routePlans)))
	case commerce.PaymentPhaseCancelled:
		body = text(locale, "payment.cancelled")
	case commerce.PaymentPhaseExpired:
		body = text(locale, "payment.expired")
		rows = append(rows, row(callbackButton(text(locale, "menu.plans"), routePlans)))
	case commerce.PaymentPhaseRefunded:
		body = text(locale, "payment.refunded", formatMoney(order.RefundedMinor, order.Currency))
	}
	for _, refund := range refunds {
		body += text(locale, "orders.refund", formatMoney(refund.AmountMinor, refund.Currency), refund.Status)
	}
	rows = append(rows,
		row(actionButton(text(locale, "action.refresh"), "order:"+order.ID)),
		row(callbackButton(text(locale, "menu.orders"), routeOrders), callbackButton(text(locale, "action.menu"), routeHome)),
	)
	return View{Text: body, Keyboard: keyboard(rows...), Protect: true}
}

// ordersView lists order history with the payment and refund state of each.
func ordersView(locale Locale, orders []OrderSummary) View {
	if len(orders) == 0 {
		return View{Text: text(locale, "orders.empty"), Keyboard: keyboard(row(callbackButton(text(locale, "menu.plans"), routePlans)), row(callbackButton(text(locale, "action.back"), routeHome))), RetryRoute: routeOrders}
	}
	lines := []string{text(locale, "orders.title"), ""}
	rows := make([][]models.InlineKeyboardButton, 0, len(orders)+1)
	for _, order := range orders {
		state := text(locale, "state."+string(order.State))
		lines = append(lines, fmt.Sprintf("• %s · <b>%s</b> · %s · %s",
			formatDate(order.CreatedAt), html.EscapeString(order.PlanName),
			formatMoney(order.SubtotalMinor-order.DiscountMinor, order.Currency), state))
		label := text(locale, "orders.row", shortID(order.ID), truncateRunes(order.PlanName, 16), state)
		rows = append(rows, row(actionButton(label, "order:"+order.ID)))
	}
	rows = append(rows, row(callbackButton(text(locale, "action.refresh"), routeOrders), callbackButton(text(locale, "action.back"), routeHome)))
	return View{Text: strings.Join(lines, "\n"), Keyboard: keyboard(rows...), RetryRoute: routeOrders}
}

// orderDetailView shows one order with its receipt and refund state.
func orderDetailView(locale Locale, order OrderSummary, refunds []RefundStatus) View {
	body := text(locale, "orders.detail", shortID(order.ID), html.EscapeString(order.PlanName),
		formatDate(order.CreatedAt), text(locale, "state."+string(order.State)),
		formatMoney(order.SubtotalMinor-order.DiscountMinor, order.Currency),
		formatMoney(order.PaidMinor, order.Currency))
	for _, refund := range refunds {
		body += text(locale, "orders.refund", formatMoney(refund.AmountMinor, refund.Currency), refund.Status)
	}
	rows := make([][]models.InlineKeyboardButton, 0, 4)
	if safeURL(order.ReceiptURL) {
		rows = append(rows, row(models.InlineKeyboardButton{Text: text(locale, "orders.receipt"), URL: order.ReceiptURL}))
	}
	if order.State == commerce.OrderPending || order.State == commerce.OrderDraft {
		rows = append(rows, row(actionButton(text(locale, "checkout.pay"), "order:"+order.ID)))
		rows = append(rows, row(actionButton(text(locale, "orders.cancel"), "order-cancel:"+order.ID)))
	}
	rows = append(rows, row(callbackButton(text(locale, "action.back"), routeOrders), callbackButton(text(locale, "action.menu"), routeHome)))
	return View{Text: body, Keyboard: keyboard(rows...), Protect: true}
}

// walletView shows the balance and the customer's own ledger history.
func walletView(locale Locale, balanceMinor int64, currency string, entries []WalletEntry, topUpEnabled bool) View {
	body := text(locale, "wallet.title", formatMoney(balanceMinor, currency))
	if len(entries) == 0 {
		body += text(locale, "wallet.empty")
	} else {
		lines := make([]string, 0, len(entries)+1)
		lines = append(lines, "")
		for _, entry := range entries {
			amount := formatMoney(entry.AmountMinor, entry.Currency)
			if entry.AmountMinor > 0 {
				amount = "+" + amount
			}
			lines = append(lines, "• "+text(locale, "wallet.entry", formatDate(entry.OccurredAt), text(locale, "wallet."+entry.Type), amount))
		}
		body += "\n" + strings.Join(lines, "\n")
	}
	rows := make([][]models.InlineKeyboardButton, 0, 3)
	if topUpEnabled {
		rows = append(rows, row(callbackButton(text(locale, "menu.topup"), routeTopUp)))
	}
	rows = append(rows, row(callbackButton(text(locale, "action.refresh"), routeWallet), callbackButton(text(locale, "action.back"), routeHome)))
	return View{Text: body, Keyboard: keyboard(rows...), RetryRoute: routeWallet}
}

// lifecycleNotice explains the current subscription phase in plain language.
func lifecycleNotice(locale Locale, phase commerce.SubscriptionPhase, entitlement Entitlement) string {
	switch phase {
	case commerce.PhaseProvisioning:
		return text(locale, "life.provisioning")
	case commerce.PhaseGrace:
		return text(locale, "life.grace", formatDate(entitlement.EndsAt), formatDate(entitlement.EndsAt.Add(entitlement.GracePeriod)))
	case commerce.PhaseLimited:
		return text(locale, "life.limited")
	case commerce.PhaseExpired:
		return text(locale, "life.expired")
	case commerce.PhaseDisabled:
		return text(locale, "life.disabled")
	case commerce.PhasePaused:
		return text(locale, "life.paused")
	case commerce.PhaseFailed:
		return text(locale, "life.failed")
	case commerce.PhaseNone:
		return text(locale, "life.none")
	default:
		return ""
	}
}

// autoRenewView reports auto-renew status, or explains honestly that no
// configured provider can charge again without a new confirmation.
func autoRenewView(locale Locale, setting AutoRenew, planName string, supported bool) View {
	if !supported {
		return View{Text: text(locale, "renew.unsupported"), Keyboard: keyboard(row(callbackButton(text(locale, "action.back"), routeSettings)))}
	}
	if setting.Enabled {
		return View{
			Text:     text(locale, "renew.on", html.EscapeString(planName), providerLabel(locale, setting.Provider)),
			Keyboard: keyboard(row(actionButton(text(locale, "renew.disable"), "autorenew:off")), row(callbackButton(text(locale, "action.back"), routeSettings))),
		}
	}
	rows := make([][]models.InlineKeyboardButton, 0, 2)
	if planName != "" {
		rows = append(rows, row(actionButton(text(locale, "renew.enable"), "autorenew:on")))
	}
	rows = append(rows, row(callbackButton(text(locale, "action.back"), routeSettings)))
	return View{Text: text(locale, "renew.off"), Keyboard: keyboard(rows...)}
}

func trafficAllowance(locale Locale, bytes *int64) string {
	if bytes == nil || *bytes <= 0 {
		return text(locale, "plans.unlimited")
	}
	return formatBytes(*bytes)
}

func deviceAllowance(locale Locale, limit *int32) string {
	if limit == nil || *limit <= 0 {
		return text(locale, "plans.unlimited")
	}
	return fmt.Sprintf("%d", *limit)
}

// shortID renders an identifier compactly without making it ambiguous: the
// first block of a UUID is enough for a customer to quote to support.
func shortID(id string) string {
	if index := strings.Index(id, "-"); index > 0 {
		return id[:index]
	}
	return truncateRunes(id, 8)
}

// channelGateView asks a customer to join the channels a purchase requires.
//
// It names each channel and links to it. "Requirements not met" is accurate and
// useless: a customer who cannot see what to do simply leaves, and the
// installation loses the sale it was trying to condition.
//
// The retry button returns to the checkout rather than starting over, because
// the checkout is still there — nothing was cancelled, and re-entering a plan,
// a promo code, and a payment method to satisfy a subscription requirement is
// how a requirement becomes an abandonment.
func channelGateView(locale Locale, gate PurchaseGate) View {
	lines := []string{text(locale, "channels.required")}
	rows := make([][]models.InlineKeyboardButton, 0, len(gate.Missing)+1)
	for _, requirement := range gate.Missing {
		lines = append(lines, "• "+html.EscapeString(requirement.Title))
		if safeURL(requirement.InviteURL) {
			rows = append(rows, row(models.InlineKeyboardButton{
				Text: text(locale, "channels.open", requirement.Title),
				URL:  requirement.InviteURL,
			}))
		}
	}
	rows = append(rows, row(actionButton(text(locale, "channels.retry"), "checkout")))
	return View{Text: strings.Join(lines, "\n"), Keyboard: keyboard(rows...)}
}
