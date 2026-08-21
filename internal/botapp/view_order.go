package botapp

import (
	"html"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/commerce"
)

// orderExtras is what the order row itself cannot say about an order: the
// gift it bought, or the delivery it paid for. Each is filled in only when the
// screen is rendered from a fresh read; a view rendered straight after
// creation leaves them empty and the copy degrades to what the order alone
// knows.
type orderExtras struct {
	Gift *SentGift
	Shop *ShopOrder
}

// orderStatusView renders an order with nothing but the order row to go on.
func orderStatusView(locale Locale, order OrderSummary, refunds []RefundStatus) View {
	return orderView(locale, order, refunds, orderExtras{})
}

// orderView renders every terminal and intermediate state of an order.
// Refreshing is always available and never loses the order.
//
// The copy follows the operation. A paid order is not "activating your
// subscription" when it was a wallet top-up, a gift, or a shop purchase: the
// money went to the wallet, the gift became redeemable, or the goods are on
// their way, and only the worker ever moves a subscription order to
// fulfilled — the others stay at paid for good and would read as activating
// forever.
func orderView(locale Locale, order OrderSummary, refunds []RefundStatus, extras orderExtras) View {
	rows := make([][]models.InlineKeyboardButton, 0, 5)
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
		if order.Operation == "gift" {
			body += "\n\n" + text(locale, "gift.awaitingPayment")
		}
		body += text(locale, "payment.delayed")
		if safeURL(order.CheckoutURL) {
			rows = append(rows, row(models.InlineKeyboardButton{Text: text(locale, "payment.open"), URL: order.CheckoutURL}))
		}
		if order.Provider == "telegram_stars" {
			rows = append(rows, row(actionButton(text(locale, "payment.invoice"), "invoice:"+order.ID)))
		}
		if orderNeedsPaymentStart(order) {
			// Nothing above can be tapped: the payment was never started, or the
			// provider refused it. The order is still open and holds its wallet
			// reservation, so the honest offer is to start the payment, not to
			// leave Cancel as the only way out.
			rows = append(rows, row(actionButton(text(locale, "checkout.pay"), "pay:"+order.ID)))
		}
		rows = append(rows, row(actionButton(text(locale, "orders.cancel"), "order-cancel:"+order.ID)))
	case commerce.PaymentPhaseSucceeded, commerce.PaymentPhaseProvisioning, commerce.PaymentPhaseCompleted:
		body, rows = settledOrderBody(locale, order, extras, rows)
	case commerce.PaymentPhaseFailed:
		if order.State == commerce.OrderPaid {
			// The payment went through; it is the provisioning that failed. Saying
			// "nothing was charged" here would be false, and the customer's next
			// move is support, not another payment.
			body = text(locale, "payment.provisionFailed")
			rows = append(rows, row(callbackButton(text(locale, "menu.support"), routeSupport)))
			break
		}
		body = text(locale, "payment.failed")
		if orderAwaitsPayment(order) {
			// The payment failed but the order did not: it can be paid another
			// way, and the copy just said so.
			rows = append(rows, row(actionButton(text(locale, "payment.otherMethod"), "pay:"+order.ID+":pick")))
			rows = append(rows, row(actionButton(text(locale, "orders.cancel"), "order-cancel:"+order.ID)))
		}
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

// settledOrderBody describes a paid order by what the money bought.
func settledOrderBody(locale Locale, order OrderSummary, extras orderExtras, rows [][]models.InlineKeyboardButton) (string, [][]models.InlineKeyboardButton) {
	switch order.Operation {
	case "topup":
		// The credit lands in the same transaction that marks the order paid,
		// so a paid top-up is a credited top-up.
		body := text(locale, "payment.topupCredited", formatMoney(order.SubtotalMinor, order.Currency))
		return body, append(rows, row(callbackButton(text(locale, "menu.wallet"), routeWallet)))
	case "gift":
		body := text(locale, "payment.giftPaid")
		if extras.Gift != nil {
			body += "\n\n" + text(locale, "payment.giftState",
				html.EscapeString(extras.Gift.CodeHint), text(locale, "gift.status."+extras.Gift.Status))
		}
		return body, append(rows, row(callbackButton(text(locale, "menu.gifts"), routeGifts)))
	case "goods":
		body := text(locale, "payment.goodsPaid")
		if extras.Shop != nil {
			body += "\n\n" + text(locale, "shop.deliveryState", shopDeliveryLabel(locale, *extras.Shop))
			if extras.Shop.DeliveryStatus == "needs_review" {
				body += "\n\n" + text(locale, "shop.underReview")
			}
		}
		return body, append(rows, row(callbackButton(text(locale, "shop.history"), routeShopOrders)))
	case "addon":
		if order.Phase == commerce.PaymentPhaseCompleted {
			return text(locale, "payment.addonApplied"), append(rows, row(callbackButton(text(locale, "menu.subs"), routeSubscriptions)))
		}
		return provisioningBody(locale, order, "payment.addonApplying"), rows
	}
	switch order.Phase {
	case commerce.PaymentPhaseSucceeded:
		return text(locale, "payment.succeeded", shortID(order.ID)), rows
	case commerce.PaymentPhaseCompleted:
		return text(locale, "payment.completed"), append(rows, row(callbackButton(text(locale, "connect.title.short"), routeConnect)))
	default:
		return provisioningBody(locale, order, "payment.provisioning"), rows
	}
}

// provisioningBody is the "being set up" copy, with an honest line added once
// the first attempt has not been enough. "Up to a minute" is true of a first
// attempt and false of a fifth, and a customer reading it for an hour learns
// only that the screen lies.
func provisioningBody(locale Locale, order OrderSummary, key string) string {
	body := text(locale, key)
	if order.FulfillmentAttempts > 1 {
		body += "\n\n" + text(locale, "payment.provisionSlow")
	}
	return body
}
