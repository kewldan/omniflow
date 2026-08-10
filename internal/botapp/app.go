package botapp

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/remnawave"
)

const (
	callbackPrefix    = "nav:"
	routeHome         = "home"
	routeSubscription = "subscription"
	routeConnect      = "connect"
	routeDevices      = "devices"
	routeSupport      = "support"
)

type App struct {
	logger     *slog.Logger
	identities IdentityStore
	remnawave  remnawave.Service
	supportURL string
}

func New(logger *slog.Logger, identities IdentityStore, remnawaveService remnawave.Service, supportURL string) *App {
	return &App{
		logger:     logger,
		identities: identities,
		remnawave:  remnawaveService,
		supportURL: strings.TrimSpace(supportURL),
	}
}

func (app *App) Register(client *telegram.Bot) {
	client.RegisterHandler(telegram.HandlerTypeMessageText, "start", telegram.MatchTypeCommandStartOnly, app.HandleStart)
	client.RegisterHandler(telegram.HandlerTypeMessageText, "menu", telegram.MatchTypeCommandStartOnly, app.HandleStart)
	client.RegisterHandler(telegram.HandlerTypeCallbackQueryData, callbackPrefix, telegram.MatchTypePrefix, app.HandleCallback)
}

func (app *App) HandleStart(ctx context.Context, client *telegram.Bot, update *models.Update) {
	message := update.Message
	if message == nil || message.From == nil || message.Chat.Type != models.ChatTypePrivate {
		return
	}
	locale := localeFrom(message.From.LanguageCode)
	_, _ = client.SendChatAction(ctx, &telegram.SendChatActionParams{ChatID: message.Chat.ID, Action: models.ChatActionTyping})
	view := app.loadView(ctx, message.From.ID, locale, routeHome)
	if _, err := client.SendMessage(ctx, sendParams(message.Chat.ID, view)); err != nil {
		app.logger.Error("telegram view send failed", "view", routeHome, "error", err)
	}
}

func (app *App) HandleDefault(ctx context.Context, client *telegram.Bot, update *models.Update) {
	if update.CallbackQuery != nil {
		app.HandleCallback(ctx, client, update)
		return
	}
	app.HandleStart(ctx, client, update)
}

func (app *App) HandleCallback(ctx context.Context, client *telegram.Bot, update *models.Update) {
	query := update.CallbackQuery
	if query == nil {
		return
	}
	locale := localeFrom(query.From.LanguageCode)
	_, _ = client.AnswerCallbackQuery(ctx, &telegram.AnswerCallbackQueryParams{CallbackQueryID: query.ID})
	if query.Message.Message == nil || query.Message.Message.Chat.Type != models.ChatTypePrivate {
		return
	}
	message := query.Message.Message
	route := strings.TrimPrefix(query.Data, callbackPrefix)
	if !knownRoute(route) {
		route = routeHome
	}
	if _, err := client.EditMessageText(ctx, editParams(message.Chat.ID, message.ID, loadingView(locale))); err != nil {
		app.logger.Debug("telegram loading state edit failed", "error", err)
	}
	view := app.loadView(ctx, query.From.ID, locale, route)
	if _, err := client.EditMessageText(ctx, editParams(message.Chat.ID, message.ID, view)); err != nil {
		app.logger.Error("telegram view edit failed", "view", route, "error", err)
	}
}

func (app *App) loadView(ctx context.Context, telegramID int64, locale Locale, route string) View {
	if route == routeSupport {
		return supportView(locale, app.supportURL)
	}
	userID, err := app.identities.RemnawaveUserID(ctx, telegramID)
	if errors.Is(err, ErrNotLinked) {
		user, lookupErr := app.remnawave.UserByTelegramID(ctx, telegramID)
		if errors.Is(lookupErr, remnawave.ErrNotFound) {
			return notLinkedView(locale, app.supportURL)
		}
		if lookupErr != nil {
			app.logger.Error("Remnawave Telegram identity lookup failed", "error", lookupErr)
			return errorView(locale, route)
		}
		userID, err = app.identities.Link(ctx, telegramID, user.ID)
		if err != nil {
			app.logger.Error("Telegram identity link failed", "error", err)
			return errorView(locale, route)
		}
	}
	if err != nil {
		app.logger.Error("telegram identity lookup failed", "error", err)
		return errorView(locale, route)
	}

	switch route {
	case routeSubscription, routeConnect:
		subscription, err := app.remnawave.Subscription(ctx, userID)
		if err != nil {
			app.logger.Error("Remnawave subscription lookup failed", "error", err)
			return errorView(locale, route)
		}
		if route == routeConnect {
			return connectView(locale, subscription)
		}
		return subscriptionView(locale, subscription)
	case routeDevices:
		user, err := app.remnawave.User(ctx, userID)
		if err != nil {
			app.logger.Error("Remnawave user lookup failed", "error", err)
			return errorView(locale, route)
		}
		devices, err := app.remnawave.Devices(ctx, userID)
		if err != nil {
			app.logger.Error("Remnawave device lookup failed", "error", err)
			return errorView(locale, route)
		}
		return devicesView(locale, devices, user.HWIDDeviceLimit)
	default:
		user, err := app.remnawave.User(ctx, userID)
		if err != nil {
			app.logger.Error("Remnawave user lookup failed", "error", err)
			return errorView(locale, routeHome)
		}
		return homeView(locale, user)
	}
}

func knownRoute(route string) bool {
	switch route {
	case routeHome, routeSubscription, routeConnect, routeDevices, routeSupport:
		return true
	default:
		return false
	}
}

func sendParams(chatID int64, view View) *telegram.SendMessageParams {
	disabled := true
	return &telegram.SendMessageParams{
		ChatID:             chatID,
		Text:               view.Text,
		ParseMode:          models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: &disabled},
		ProtectContent:     true,
		ReplyMarkup:        view.Keyboard,
	}
}

func editParams(chatID int64, messageID int, view View) *telegram.EditMessageTextParams {
	disabled := true
	return &telegram.EditMessageTextParams{
		ChatID:             chatID,
		MessageID:          messageID,
		Text:               view.Text,
		ParseMode:          models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: &disabled},
		ReplyMarkup:        view.Keyboard,
	}
}
