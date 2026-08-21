package accountcheckout

import (
	"context"
	"net/url"
	"strings"
)

// Telegram Stars settles only inside a Telegram chat: the bot sends an
// invoice and the customer pays it there. From a browser that means two
// things, both handled here. The method is offered only to a customer the bot
// can actually reach — one with a Telegram identity — and a Stars payment's
// handoff is a deep link that opens the bot with `/start pay_<orderID>`, which
// is the bot's cue to send the invoice for that order.

// StarsInvoiceLink is the deep link that makes the bot send the invoice for
// one order.
func StarsInvoiceLink(botUsername, orderID string) string {
	botUsername = strings.TrimPrefix(strings.TrimSpace(botUsername), "@")
	if botUsername == "" || orderID == "" {
		return ""
	}
	return "https://t.me/" + url.PathEscape(botUsername) + "?start=pay_" + url.QueryEscape(orderID)
}

// SetBotUsername attaches the resolver for the bot's own @name, which the
// Stars handoff is built from. It is the same resolver the sign-in screen
// uses, so the two never name different bots.
func (service *Service) SetBotUsername(resolve func(context.Context) string) {
	service.botUsername = resolve
}

// Handoff decides how a payment is presented to the customer: the kind, and
// the link to follow.
//
// A hosted page and a manual transfer are what the adapter said they were. A
// Stars intent carries no URL of its own, so it is presented as a Telegram
// invoice reachable through the bot link when the bot's name resolves — and
// as `none` when it does not, which the page treats as "finish this in the
// bot" rather than "nothing left to pay" whenever the order still owes money.
func (service *Service) Handoff(ctx context.Context, provider, checkoutURL, orderID string) (kind, link string) {
	if provider == ProviderTelegramStars && checkoutURL == "" && service.botUsername != nil {
		if invoice := StarsInvoiceLink(service.botUsername(ctx), orderID); invoice != "" {
			return HandoffTelegram, invoice
		}
	}
	return HandoffFor(provider, checkoutURL), checkoutURL
}

// WithoutStars drops the Telegram Stars method from a list of choices.
func WithoutStars(choices []PaymentChoice) []PaymentChoice {
	kept := make([]PaymentChoice, 0, len(choices))
	for _, choice := range choices {
		if choice.Provider == ProviderTelegramStars {
			continue
		}
		kept = append(kept, choice)
	}
	return kept
}

// forCustomer narrows a list of payment choices to what this customer can
// actually use: Stars only when the bot can reach them.
func (service *Service) forCustomer(ctx context.Context, customerID string, choices []PaymentChoice) ([]PaymentChoice, error) {
	linked, err := service.store.HasTelegramIdentity(ctx, customerID)
	if err != nil {
		return nil, err
	}
	if linked {
		return choices, nil
	}
	return WithoutStars(choices), nil
}

// HasTelegramIdentity reports whether the customer holds an active Telegram
// identity — the condition for anything that has to reach them in a chat.
func (store *Store) HasTelegramIdentity(ctx context.Context, customerID string) (bool, error) {
	var linked bool
	err := store.pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM identities
			WHERE user_id = $1::uuid AND provider = 'telegram' AND status = 'active'
		)`, customerID).Scan(&linked)
	return linked, err
}
