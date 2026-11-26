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
	// AttachMagicLinkMessage records the message a link was delivered in, so
	// signing in with it deletes the message from the chat.
	AttachMagicLinkMessage(ctx context.Context, link string, chatID int64, messageID int) error
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
// The link is a URL button under the warning rather than a raw address in the
// text: a token printed in the message is easy to copy by accident and reads
// as noise. Once the link is spent, the API deletes the message, so the chat
// is left with no credential, used or not, for anybody to find later.
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
	if !accountActive(customer) {
		// The web panel refuses a suspended or deleted account at the door; a
		// link that can only lead there is not worth minting.
		_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, accountUnavailableView(locale, customer, app.supportLink())))
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

	// The link is a bearer credential for ten minutes. The message is not sent
	// as protected content: Telegram would then block screenshots of the chat,
	// and the bot never does that.
	//
	// The delivery error is reported rather than discarded. This is the one
	// branch that has to reach the customer, and a send that fails here looks
	// from the outside exactly like a command that was never received: no
	// message arrives, and until this line existed nothing was written down
	// either. The link itself is never logged — it is a credential.
	sent, err := client.SendMessage(ctx, sendParams(message.Chat.ID, View{
		Text: text(locale, "weblogin.link"),
		Keyboard: keyboard(row(models.InlineKeyboardButton{
			Text: text(locale, "weblogin.button"), URL: link,
		})),
	}))
	if err != nil {
		app.logger.Warn("web sign-in link could not be delivered", "error", err)
		return
	}
	if err := app.webSignIn.AttachMagicLinkMessage(ctx, link, message.Chat.ID, sent.ID); err != nil {
		// The link still works; only the clean-up after it is lost.
		app.logger.Warn("web sign-in message could not be recorded", "error", err)
	}
}

func (app *App) sendWebLoginNotice(
	ctx context.Context, client *telegram.Bot, chatID int64, locale Locale, key string,
) {
	// Warn rather than Debug. Every one of these notices is the only thing the
	// customer will see, so a delivery that fails leaves them looking at a chat
	// where nothing happened — and a level nobody runs at leaves the operator
	// with nothing to look at either.
	if _, err := client.SendMessage(ctx, sendParams(chatID, View{Text: text(locale, key)})); err != nil {
		app.logger.Warn("web sign-in notice could not be delivered", "notice", key, "error", err)
	}
}
