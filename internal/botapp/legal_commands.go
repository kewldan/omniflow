package botapp

import (
	"context"
	"strings"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// HandleTerms answers /terms, which Telegram requires of a bot that sells
// for Stars: the offer the customer agrees to by paying.
func (app *App) HandleTerms(ctx context.Context, client *telegram.Bot, update *models.Update) {
	message := update.Message
	if message == nil || message.From == nil {
		return
	}
	locale := app.locale(ctx, message.From.ID, message.From.LanguageCode)
	view := View{Text: text(locale, "legal.termsMissing")}
	if url := app.termsLink(); url != "" {
		view = View{
			Text:     text(locale, "legal.terms"),
			Keyboard: keyboard(row(models.InlineKeyboardButton{Text: text(locale, "legal.openTerms"), URL: url})),
		}
	}
	_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, view))
}

// HandlePaySupport answers /paysupport, which Telegram requires of a bot that
// sells for Stars: where a customer goes with a payment problem, and what
// happens to a payment that bought nothing.
func (app *App) HandlePaySupport(ctx context.Context, client *telegram.Bot, update *models.Update) {
	message := update.Message
	if message == nil || message.From == nil {
		return
	}
	locale := app.locale(ctx, message.From.ID, message.From.LanguageCode)
	rows := [][]models.InlineKeyboardButton{row(callbackButton(text(locale, "legal.openSupport"), routeSupport))}
	if app.supportURL != "" {
		rows = append(rows, row(models.InlineKeyboardButton{Text: text(locale, "legal.contact"), URL: app.supportURL}))
	}
	_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, View{
		Text: text(locale, "legal.paySupport"), Keyboard: &models.InlineKeyboardMarkup{InlineKeyboard: rows},
	}))
}

// HandleFAQ answers /faq with the installation's published information pages,
// where an operator keeps instructions and answers.
func (app *App) HandleFAQ(ctx context.Context, client *telegram.Bot, update *models.Update) {
	message := update.Message
	if message == nil || message.From == nil {
		return
	}
	locale := app.locale(ctx, message.From.ID, message.From.LanguageCode)
	base := strings.TrimRight(strings.TrimSpace(app.settings.PublicURL), "/")
	if base == "" {
		app.handleCommandRoute(ctx, client, update, routeSupport)
		return
	}
	_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, View{
		Text: text(locale, "legal.faq"),
		Keyboard: keyboard(
			row(models.InlineKeyboardButton{Text: text(locale, "legal.openFAQ"), URL: base + "/pages"}),
			row(callbackButton(text(locale, "legal.openSupport"), routeSupport)),
		),
	}))
}

// termsLink is the configured offer, or the installation's published pages.
func (app *App) termsLink() string {
	if app.termsURL != "" {
		return app.termsURL
	}
	if base := strings.TrimRight(strings.TrimSpace(app.settings.PublicURL), "/"); base != "" {
		return base + "/pages"
	}
	return ""
}
