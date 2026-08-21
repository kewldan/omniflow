package botapp

import (
	"github.com/go-telegram/bot/models"
)

// Suspended and deleted customers.
//
// The customer record carries a status, and the web panel answers a suspended
// or deleted customer with a 403 and an explanation. The bot read the status
// and ignored it: a customer an operator had suspended kept the full surface —
// purchases, wallet, link rotation, device deletion. This is the bot's side of
// the same rule.
//
// Support stays open. An operator suspends a customer to talk to them, and a
// customer who has been suspended wants to talk to an operator; closing the
// ticket list would leave both staring at a wall. Everything else — screens
// that act on the subscription or move money — is replaced by one short screen
// that says what happened and offers support.

// accountActive reports whether the customer may use the bot's full surface.
// An empty status is the pre-commerce shape and is treated as active.
func accountActive(customer Customer) bool {
	return customer.Status == "" || customer.Status == "active"
}

// allowedWhileInactive is the closed set of routes and actions a suspended or
// deleted customer keeps: the ticket list, one ticket, and writing into it.
func allowedWhileInactive(name string) bool {
	switch name {
	// routeSupport doubles as the v0.2 "support" action name.
	case routeSupport, "ticket", "ticket-reply", "support-new":
		return true
	default:
		return false
	}
}

// accountUnavailableView is the one screen an inactive customer sees. It names
// the state — suspended and deleted read differently to the person reading
// them — and offers the support desk, which keeps working.
func accountUnavailableView(locale Locale, customer Customer, supportURL string) View {
	key := "account.suspended"
	if customer.Status == "deleted" {
		key = "account.deleted"
	}
	rows := make([][]models.InlineKeyboardButton, 0, 2)
	rows = append(rows, row(callbackButton(text(locale, "menu.support"), routeSupport)))
	if safeURL(supportURL) {
		rows = append(rows, row(models.InlineKeyboardButton{Text: text(locale, "account.contact"), URL: supportURL}))
	}
	return View{Text: text(locale, key), Keyboard: keyboard(rows...)}
}
