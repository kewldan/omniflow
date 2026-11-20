package botapp

import "github.com/go-telegram/bot/models"

// restrictedView answers an order an operator stopped on the customer card:
// no top-up, or no purchase. It names no reason — that is the operator's note
// — and points at support, where the customer can ask.
func restrictedView(locale Locale) View {
	return View{
		Text: text(locale, "restricted.payment"),
		Keyboard: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			row(callbackButton(text(locale, "legal.openSupport"), routeSupport)),
			row(callbackButton(text(locale, "action.back"), routeHome)),
		}},
	}
}
