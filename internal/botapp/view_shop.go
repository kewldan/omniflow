package botapp

import (
	"fmt"
	"html"

	"github.com/go-telegram/bot/models"
)

// shopCatalogView lists what the operator sells, grouped by kind.
//
// Premium and Stars are separated because they are answers to different
// questions — a duration versus a quantity — and interleaving them makes both
// lists harder to scan.
func shopCatalogView(locale Locale, products []ShopProduct) View {
	if len(products) == 0 {
		return View{
			Text:     text(locale, "shop.empty"),
			Keyboard: keyboard(row(callbackButton(text(locale, "action.menu"), routeHome))),
		}
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(products)+2)
	premium := filterKind(products, "telegram_premium")
	stars := filterKind(products, "telegram_stars")
	body := text(locale, "shop.title")
	if len(premium) > 0 {
		body += "\n\n" + text(locale, "shop.premiumSection")
		for _, product := range premium {
			rows = append(rows, row(actionButton(
				shopButtonLabel(locale, product), "shop-item:"+product.ID)))
		}
	}
	if len(stars) > 0 {
		body += "\n" + text(locale, "shop.starsSection")
		for _, product := range stars {
			rows = append(rows, row(actionButton(
				shopButtonLabel(locale, product), "shop-item:"+product.ID)))
		}
	}
	rows = append(rows,
		row(callbackButton(text(locale, "shop.history"), routeShopOrders)),
		row(callbackButton(text(locale, "action.menu"), routeHome)),
	)
	return View{Text: body, Keyboard: keyboard(rows...)}
}

// shopButtonLabel names one catalog row.
//
// A product whose price depends on a live provider rate carries no price here:
// showing one would mean quoting every product on every catalog open, and a
// number that moves between the list and the detail screen is worse than no
// number at all.
func shopButtonLabel(locale Locale, product ShopProduct) string {
	label := product.Name
	if product.PriceKnown {
		return fmt.Sprintf("%s · %s", label, formatMoney(product.PriceMinor, product.Currency))
	}
	return label
}

// shopItemView is one product with a quoted price and the two ways to buy it.
func shopItemView(locale Locale, product ShopProduct, quote ShopQuote, quoteFailed bool) View {
	body := text(locale, "shop.item", html.EscapeString(product.Name))
	if product.Description != "" {
		body += "\n\n" + html.EscapeString(product.Description)
	}
	body += "\n\n" + shopAmountLine(locale, product)
	if quoteFailed {
		// A price that could not be quoted is stated as such. Offering a buy
		// button that fails at confirmation would waste the customer's time and
		// teach them the shop is unreliable.
		body += "\n\n" + text(locale, "shop.quoteFailed")
		return View{Text: body, Keyboard: keyboard(
			row(callbackButton(text(locale, "action.back"), routeShop)),
		)}
	}
	body += "\n" + text(locale, "shop.price", formatMoney(quote.PriceMinor, quote.Currency))
	if !quote.ExpiresAt.IsZero() {
		body += "\n" + text(locale, "shop.quoteExpiry", formatDate(quote.ExpiresAt))
	}
	return View{Text: body, Keyboard: keyboard(
		row(actionButton(text(locale, "shop.buyForMe"), "shop-self:"+product.ID)),
		row(actionButton(text(locale, "shop.buyForOther"), "shop-other:"+product.ID)),
		row(callbackButton(text(locale, "action.back"), routeShop)),
	)}
}

// shopAmountLine states what is actually being bought: months for Premium, a
// count for Stars.
func shopAmountLine(locale Locale, product ShopProduct) string {
	if product.Kind == "telegram_premium" {
		return text(locale, "shop.months", product.DurationMonths)
	}
	return text(locale, "shop.stars", product.StarQuantity)
}

// shopRecipientPromptView asks for the username the goods go to.
func shopRecipientPromptView(locale Locale) View {
	return View{
		Text:     text(locale, "shop.recipientPrompt"),
		Keyboard: keyboard(row(callbackButton(text(locale, "action.cancel"), routeShop))),
	}
}

// shopConfirmView is the recipient review step.
//
// The recipient is restated in full, on its own line, immediately above the
// button that spends the money. Digital goods cannot be recalled once a gateway
// has sent them, so a mistyped username is unrecoverable — which is exactly why
// this screen exists rather than buying straight from the product page.
func shopConfirmView(
	locale Locale, product ShopProduct, quote ShopQuote, recipient string, isSelf bool,
) View {
	body := text(locale, "shop.confirmTitle") +
		"\n\n" + text(locale, "shop.confirmProduct", html.EscapeString(product.Name)) +
		"\n" + shopAmountLine(locale, product) +
		"\n" + text(locale, "shop.price", formatMoney(quote.PriceMinor, quote.Currency))
	if isSelf {
		body += "\n\n" + text(locale, "shop.confirmSelf", html.EscapeString(recipient))
	} else {
		body += "\n\n" + text(locale, "shop.confirmOther", html.EscapeString(recipient))
	}
	body += "\n\n" + text(locale, "shop.irreversible")
	return View{Text: body, Keyboard: keyboard(
		row(actionButton(text(locale, "shop.confirmBuy"), fmt.Sprintf("shop-buy:%s:%s", product.ID, recipient))),
		row(callbackButton(text(locale, "action.cancel"), routeShop)),
	)}
}

// shopOrdersView is the customer's shop history.
func shopOrdersView(locale Locale, orders []ShopOrder) View {
	if len(orders) == 0 {
		return View{
			Text:     text(locale, "shop.noOrders"),
			Keyboard: keyboard(row(callbackButton(text(locale, "action.back"), routeShop))),
		}
	}
	body := text(locale, "shop.ordersTitle")
	rows := make([][]models.InlineKeyboardButton, 0, len(orders)+1)
	for _, order := range orders {
		body += fmt.Sprintf("\n\n<b>%s</b>\n%s · %s\n%s",
			html.EscapeString(order.ProductName),
			formatMoney(order.PriceMinor, order.Currency),
			shopDeliveryLabel(locale, order),
			text(locale, "shop.recipientLine", html.EscapeString(order.Recipient)))
		rows = append(rows, row(actionButton(
			shortID(order.OrderID)+" · "+shopDeliveryLabel(locale, order),
			"shop-order:"+order.OrderID)))
	}
	rows = append(rows, row(callbackButton(text(locale, "action.back"), routeShop)))
	return View{Text: body, Keyboard: keyboard(rows...)}
}

// shopOrderView is one purchase with its delivery progress.
func shopOrderView(locale Locale, order ShopOrder) View {
	body := text(locale, "shop.orderTitle", shortID(order.OrderID)) +
		"\n\n" + text(locale, "shop.confirmProduct", html.EscapeString(order.ProductName)) +
		"\n" + text(locale, "shop.price", formatMoney(order.PriceMinor, order.Currency)) +
		"\n" + text(locale, "shop.recipientLine", html.EscapeString(order.Recipient)) +
		"\n\n" + text(locale, "shop.deliveryState", shopDeliveryLabel(locale, order))
	if order.DeliveryStatus == "needs_review" {
		// Ambiguous is the honest word. The purchase may have gone through, and
		// telling the customer it failed would be as wrong as telling them it
		// succeeded.
		body += "\n\n" + text(locale, "shop.underReview")
	}
	return View{Text: body, Keyboard: keyboard(
		row(callbackButton(text(locale, "action.refresh"), routeShopOrders)),
		row(callbackButton(text(locale, "action.back"), routeShopOrders)),
	)}
}

// shopDeliveryLabel describes the state of the goods rather than of the
// payment. A customer asking "where are my Stars" is not asking whether their
// card was charged.
func shopDeliveryLabel(locale Locale, order ShopOrder) string {
	switch {
	case order.Status == "refunded":
		return text(locale, "shop.state.refunded")
	case order.DeliveryStatus == "delivered":
		return text(locale, "shop.state.delivered")
	case order.DeliveryStatus == "needs_review":
		return text(locale, "shop.state.review")
	case order.DeliveryStatus == "failed":
		return text(locale, "shop.state.failed")
	case order.DeliveryStatus != "":
		return text(locale, "shop.state.delivering")
	case order.Status == "quoted":
		return text(locale, "shop.state.awaitingPayment")
	default:
		return text(locale, "shop.state.paid")
	}
}

func filterKind(products []ShopProduct, kind string) []ShopProduct {
	filtered := make([]ShopProduct, 0, len(products))
	for _, product := range products {
		if product.Kind == kind {
			filtered = append(filtered, product)
		}
	}
	return filtered
}
