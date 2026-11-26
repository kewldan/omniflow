package botapp

import (
	"context"
	"strings"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/termsgate"
)

// termsAcceptAction is the button that records the customer's acceptance.
const termsAcceptAction = "terms-accept"

// termsActions are owned by termsMiddleware, which answers them before the
// callback handler runs.
var termsActions = map[string]bool{termsAcceptAction: true}

// termsGateView asks the customer to accept the terms before anything else,
// with a button for each document they agree to. A document opens on the
// public site when the bot knows its address, where it is read whole; inside
// Telegram otherwise. With no document published, the one configured terms
// address stands in for them.
func termsGateView(locale Locale, documents []termsgate.Document, publicURL, termsURL string) View {
	rows := make([][]models.InlineKeyboardButton, 0, len(documents)+1)
	body := text(locale, "terms.gate")
	base := strings.TrimRight(strings.TrimSpace(publicURL), "/")
	for _, document := range documents {
		if document.Kind == "privacy" {
			body += "\n\n" + text(locale, "terms.gatePrivacy")
		}
		label := "📄 " + truncateRunes(document.Title, 40)
		if address := base + "/pages/" + document.Slug; base != "" && safeURL(address) {
			rows = append(rows, row(models.InlineKeyboardButton{Text: label, URL: address}))
		} else {
			rows = append(rows, row(actionButton(label, "page:"+document.Slug)))
		}
	}
	if len(documents) == 0 && safeURL(termsURL) {
		rows = append(rows, row(models.InlineKeyboardButton{Text: text(locale, "terms.read"), URL: termsURL}))
	}
	rows = append(rows, row(actionButton(text(locale, "terms.accept"), termsAcceptAction)))
	return View{Text: body, Keyboard: keyboard(rows...)}
}

// termsGateScreen reads the documents the gate shows. An unreadable list
// still shows the gate, with the configured terms address.
func (app *App) termsGateScreen(ctx context.Context, locale Locale) View {
	documents, err := app.terms.Documents(ctx, string(locale))
	if err != nil {
		app.logger.Warn("terms documents could not be read", "error", err)
		documents = nil
	}
	return termsGateView(locale, documents, app.settings.PublicURL, app.configuredTerms())
}

// termsPassThrough reports whether a button works before the terms are
// accepted: reading a document the gate links to inside Telegram. Its back
// button leads to the pages list, which the gate answers with itself again.
func termsPassThrough(data string) bool {
	return strings.HasPrefix(data, actionPrefix+"page:")
}

// termsMiddleware stops every menu, command, and button at the terms gate
// until the customer accepts the current version, when the Marketing settings
// require it. The accept button itself, the documents it links to, and the
// commands that explain the service (/terms, /paysupport, /faq), pass through.
func (app *App) termsMiddleware(next telegram.HandlerFunc) telegram.HandlerFunc {
	return func(ctx context.Context, client *telegram.Bot, update *models.Update) {
		if !app.commerceEnabled() || app.terms == nil {
			next(ctx, client, update)
			return
		}
		var (
			from   *models.User
			chatID int64
			query  *models.CallbackQuery
		)
		switch {
		case update.Message != nil && update.Message.From != nil:
			from, chatID = update.Message.From, update.Message.Chat.ID
		case update.CallbackQuery != nil && update.CallbackQuery.Message.Message != nil:
			query = update.CallbackQuery
			from, chatID = &query.From, query.Message.Message.Chat.ID
		default:
			next(ctx, client, update)
			return
		}
		policy := app.terms.Policy(ctx)
		if !policy.Required {
			next(ctx, client, update)
			return
		}
		locale := app.locale(ctx, from.ID, from.LanguageCode)
		session, err := app.commerceContext(ctx, from.ID, from.LanguageCode)
		if err != nil {
			app.logger.Warn("customer resolution failed at the terms gate", "error", err)
			next(ctx, client, update)
			return
		}
		if query != nil && query.Data == actionPrefix+termsAcceptAction {
			_, _ = client.AnswerCallbackQuery(ctx, &telegram.AnswerCallbackQueryParams{CallbackQueryID: query.ID})
			if err := app.terms.Accept(ctx, session.Customer.ID, policy.Version, "telegram_bot", ""); err != nil {
				app.logError(ctx, "terms acceptance could not be recorded", "error", err)
				return
			}
			view := app.loadView(ctx, from.ID, locale, routeHome)
			_, _ = client.EditMessageText(ctx, editParams(chatID, query.Message.Message.ID, view))
			return
		}
		if query != nil && termsPassThrough(query.Data) {
			next(ctx, client, update)
			return
		}
		accepted, err := app.terms.Accepted(ctx, session.Customer.ID, policy.Version)
		if err != nil {
			app.logger.Warn("terms acceptance lookup failed", "error", err)
		}
		if accepted || err != nil {
			next(ctx, client, update)
			return
		}
		view := app.termsGateScreen(ctx, locale)
		if query != nil {
			_, _ = client.AnswerCallbackQuery(ctx, &telegram.AnswerCallbackQueryParams{CallbackQueryID: query.ID})
			_, _ = client.EditMessageText(ctx, editParams(chatID, query.Message.Message.ID, view))
			return
		}
		_, _ = client.SendMessage(ctx, sendParams(chatID, view))
	}
}

// termsBlocked reports whether the customer still has to accept the terms.
func (app *App) termsBlocked(ctx context.Context, from *models.User) bool {
	if !app.commerceEnabled() || app.terms == nil || from == nil {
		return false
	}
	session, err := app.commerceContext(ctx, from.ID, from.LanguageCode)
	if err != nil {
		return false
	}
	blocked, _, err := app.terms.Blocked(ctx, session.Customer.ID)
	return err == nil && blocked
}

// useTermsGate is wired with the commerce surface.
func (app *App) useTermsGate(gate *termsgate.Gate) { app.terms = gate }
