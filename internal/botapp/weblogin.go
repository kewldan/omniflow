package botapp

import (
	"context"
	"errors"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/customerauth"
)

// WebSignIn issues a one-time sign-in link for the customer web panel.
//
// The bot is the delivery channel for that link, which is a deliberate design
// choice rather than a limitation. A sign-in form on the web would have to take
// some identifier and answer whether it is known here, and every version of that
// answer is an enumeration oracle. Starting the flow from inside a chat the
// customer has already authenticated to removes the question, and guarantees the
// link reaches somebody who already controls the account.
type WebSignIn interface {
	IssueMagicLink(ctx context.Context, customerID string) (string, error)
}

// WithWebSignIn attaches the customer web sign-in issuer. Leaving it nil is the
// normal state for an installation whose operator has not enabled the fallback;
// the command then reports that the route is unavailable.
func (app *App) WithWebSignIn(issuer WebSignIn) *App {
	app.webSignIn = issuer
	return app
}

// HandleWebLogin answers /login with a one-time link into the web panel.
//
// The link is sent as plain text rather than as a button. A Telegram inline
// button would open the link in the in-app browser, which on several platforms
// holds a separate cookie jar from the customer's real browser — they would sign
// in somewhere they are not going to come back to.
func (app *App) HandleWebLogin(ctx context.Context, client *telegram.Bot, update *models.Update) {
	message := update.Message
	if message == nil || message.From == nil || message.Chat.Type != models.ChatTypePrivate {
		return
	}
	locale := app.locale(ctx, message.From.ID, message.From.LanguageCode)

	if app.webSignIn == nil || app.customers == nil {
		app.sendWebLoginNotice(ctx, client, message.Chat.ID, locale, "weblogin.unavailable")
		return
	}
	// The same per-user budget the rest of the bot uses. It is the first of two
	// limits: this one bounds how often the command may be run at all, and the
	// identity service separately bounds how many links one account may be sent.
	if !app.allow(ctx, "bot:weblogin", message.From.ID, callbackBudget) {
		app.sendWebLoginNotice(ctx, client, message.Chat.ID, locale, "menu.rateLimit")
		return
	}

	customer, err := app.customers.EnsureCustomer(ctx, message.From.ID, message.From.LanguageCode)
	if err != nil {
		app.logger.Warn("web sign-in link could not resolve the customer", "error", err)
		app.sendWebLoginNotice(ctx, client, message.Chat.ID, locale, "weblogin.failed")
		return
	}

	link, err := app.webSignIn.IssueMagicLink(ctx, customer.ID)
	switch {
	case errors.Is(err, customerauth.ErrMagicLinkUnavailable):
		app.sendWebLoginNotice(ctx, client, message.Chat.ID, locale, "weblogin.unavailable")
		return
	case errors.Is(err, customerauth.ErrMagicLinkThrottled):
		app.sendWebLoginNotice(ctx, client, message.Chat.ID, locale, "weblogin.throttled")
		return
	case err != nil:
		app.logger.Warn("web sign-in link could not be issued", "error", err)
		app.sendWebLoginNotice(ctx, client, message.Chat.ID, locale, "weblogin.failed")
		return
	}

	// Protected so the message cannot be forwarded: the link is a bearer
	// credential for ten minutes, and forwarding it hands over the account.
	_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, View{
		Text: text(locale, "weblogin.link", link), Protect: true,
	}))
}

func (app *App) sendWebLoginNotice(
	ctx context.Context, client *telegram.Bot, chatID int64, locale Locale, key string,
) {
	if _, err := client.SendMessage(ctx, sendParams(chatID, View{Text: text(locale, key)})); err != nil {
		app.logger.Debug("web sign-in notice delivery failed", "error", err)
	}
}
