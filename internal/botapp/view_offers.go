package botapp

import (
	"fmt"
	"html"
	"time"

	"github.com/go-telegram/bot/models"
)

// offersView lists the offers a customer may still take.
//
// Each one carries its own countdown and its own dismissal. The countdown is in
// whole days rather than hours: an offer measured in hours reads as pressure
// rather than as information, and the operator's window is days wide anyway.
func offersView(locale Locale, offers []CustomerOffer, now time.Time) View {
	if len(offers) == 0 {
		return View{
			Text:     text(locale, "offer.empty"),
			Keyboard: keyboard(row(callbackButton(text(locale, "action.menu"), routeHome))),
		}
	}
	body := text(locale, "offer.title")
	rows := make([][]models.InlineKeyboardButton, 0, len(offers)*2+1)
	for _, offer := range offers {
		body += fmt.Sprintf("\n\n🎁 <b>%s</b>\n%s",
			html.EscapeString(offer.Title), offerCountdown(locale, offer.ExpiresAt, now))
		rows = append(rows, row(
			actionButton(text(locale, "offer.open"), "offer-take:"+offer.ID),
			actionButton(text(locale, "offer.dismiss"), "offer-dismiss:"+offer.ID),
		))
	}
	rows = append(rows, row(callbackButton(text(locale, "action.menu"), routeHome)))
	return View{Text: body, Keyboard: keyboard(rows...)}
}

// offerDetailView shows one offer with its terms and the code to use.
//
// The promo code is shown rather than applied silently, so the customer can see
// what is being taken off and the checkout stays the single place a discount is
// computed.
func offerDetailView(locale Locale, offer CustomerOffer, now time.Time) View {
	body := fmt.Sprintf("🎁 <b>%s</b>", html.EscapeString(offer.Title))
	if offer.Terms != "" {
		body += "\n\n" + html.EscapeString(offer.Terms)
	}
	body += "\n\n" + offerCountdown(locale, offer.ExpiresAt, now)
	if offer.PromoCode != "" {
		body += "\n" + text(locale, "offer.code", html.EscapeString(offer.PromoCode))
	}
	body += "\n\n" + text(locale, "offer.singleUse")
	return View{Text: body, Keyboard: keyboard(
		row(callbackButton(text(locale, "menu.plans"), routePlans)),
		row(actionButton(text(locale, "offer.dismiss"), "offer-dismiss:"+offer.ID)),
		row(callbackButton(text(locale, "action.back"), routeOffers)),
	)}
}

// offerCountdown phrases the remaining window.
//
// The last day is named rather than counted, because "0 days left" is a number
// that tells a customer nothing about whether they still have time.
func offerCountdown(locale Locale, expiresAt, now time.Time) string {
	remaining := expiresAt.Sub(now)
	if remaining <= 0 {
		return text(locale, "offer.expired")
	}
	days := int((remaining + 23*time.Hour) / (24 * time.Hour))
	if days <= 1 {
		return text(locale, "offer.lastDay")
	}
	return text(locale, "offer.remaining", days)
}
