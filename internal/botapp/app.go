package botapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/remnawave"
)

const (
	callbackPrefix    = "nav:"
	actionPrefix      = "act:"
	routeHome         = "home"
	routeSubscription = "subscription"
	routeConnect      = "connect"
	routeDevices      = "devices"
	routeSupport      = "support"
	routeSettings     = "settings"
	routeReferral     = "referral"
)

type App struct {
	logger      *slog.Logger
	store       Store
	remnawave   remnawave.Service
	supportURL  string
	botUsername string
}

func New(logger *slog.Logger, store Store, remnawaveService remnawave.Service, supportURL string) *App {
	return &App{
		logger:     logger,
		store:      store,
		remnawave:  remnawaveService,
		supportURL: strings.TrimSpace(supportURL),
	}
}

func (app *App) SetBotUsername(username string) {
	app.botUsername = strings.TrimPrefix(strings.TrimSpace(username), "@")
}

func (app *App) Register(client *telegram.Bot) {
	client.RegisterHandler(telegram.HandlerTypeMessageText, "start", telegram.MatchTypeCommandStartOnly, app.HandleStart)
	client.RegisterHandler(telegram.HandlerTypeMessageText, "menu", telegram.MatchTypeCommandStartOnly, app.HandleStart)
	client.RegisterHandler(telegram.HandlerTypeMessageText, "settings", telegram.MatchTypeCommandStartOnly, app.HandleSettings)
	client.RegisterHandler(telegram.HandlerTypeMessageText, "support", telegram.MatchTypeCommandStartOnly, app.HandleSupport)
	client.RegisterHandler(telegram.HandlerTypeMessageText, "cancel", telegram.MatchTypeCommandStartOnly, app.HandleCancel)
	client.RegisterHandler(telegram.HandlerTypeCallbackQueryData, callbackPrefix, telegram.MatchTypePrefix, app.HandleCallback)
	client.RegisterHandler(telegram.HandlerTypeCallbackQueryData, actionPrefix, telegram.MatchTypePrefix, app.HandleCallback)
}

func (app *App) HandleStart(ctx context.Context, client *telegram.Bot, update *models.Update) {
	message := update.Message
	if message == nil || message.From == nil || message.Chat.Type != models.ChatTypePrivate {
		return
	}
	locale := app.locale(ctx, message.From.ID, message.From.LanguageCode)
	_, _ = client.SendChatAction(ctx, &telegram.SendChatActionParams{ChatID: message.Chat.ID, Action: models.ChatActionTyping})
	view := app.loadView(ctx, message.From.ID, locale, routeHome)
	if parts := strings.Fields(message.Text); len(parts) == 2 && strings.HasPrefix(parts[1], "ref_") {
		if err := app.store.AttributeReferral(ctx, message.From.ID, strings.TrimPrefix(parts[1], "ref_")); err != nil {
			app.logger.Warn("referral attribution failed", "error", err)
		}
	}
	if _, err := client.SendMessage(ctx, sendParams(message.Chat.ID, view)); err != nil {
		app.logger.Error("telegram view send failed", "view", routeHome, "error", err)
	}
}

func (app *App) HandleDefault(ctx context.Context, client *telegram.Bot, update *models.Update) {
	if update.CallbackQuery != nil {
		app.HandleCallback(ctx, client, update)
		return
	}
	message := update.Message
	if message == nil || message.From == nil || message.Chat.Type != models.ChatTypePrivate {
		return
	}
	state, err := app.store.Session(ctx, message.From.ID)
	if err != nil {
		app.logger.Error("telegram session lookup failed", "error", err)
	}
	if state == "support_message" && !strings.HasPrefix(message.Text, "/") {
		locale := app.locale(ctx, message.From.ID, message.From.LanguageCode)
		if err := app.store.SubmitSupport(ctx, message.From.ID, message.ID, message.Text); err != nil {
			_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, supportSubmitErrorView(locale)))
			return
		}
		_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, supportSubmittedView(locale)))
		return
	}
	app.HandleStart(ctx, client, update)
}

func (app *App) HandleSettings(ctx context.Context, client *telegram.Bot, update *models.Update) {
	app.handleCommandRoute(ctx, client, update, routeSettings)
}

func (app *App) HandleSupport(ctx context.Context, client *telegram.Bot, update *models.Update) {
	app.handleCommandRoute(ctx, client, update, routeSupport)
}

func (app *App) HandleCancel(ctx context.Context, client *telegram.Bot, update *models.Update) {
	if update.Message == nil || update.Message.From == nil {
		return
	}
	_ = app.store.CancelSession(ctx, update.Message.From.ID)
	app.handleCommandRoute(ctx, client, update, routeHome)
}

func (app *App) handleCommandRoute(ctx context.Context, client *telegram.Bot, update *models.Update, route string) {
	message := update.Message
	if message == nil || message.From == nil || message.Chat.Type != models.ChatTypePrivate {
		return
	}
	locale := app.locale(ctx, message.From.ID, message.From.LanguageCode)
	_, _ = client.SendMessage(ctx, sendParams(message.Chat.ID, app.loadView(ctx, message.From.ID, locale, route)))
}

func (app *App) HandleCallback(ctx context.Context, client *telegram.Bot, update *models.Update) {
	query := update.CallbackQuery
	if query == nil {
		return
	}
	locale := app.locale(ctx, query.From.ID, query.From.LanguageCode)
	_, _ = client.AnswerCallbackQuery(ctx, &telegram.AnswerCallbackQueryParams{CallbackQueryID: query.ID})
	if query.Message.Message == nil || query.Message.Message.Chat.Type != models.ChatTypePrivate {
		return
	}
	message := query.Message.Message
	if strings.HasPrefix(query.Data, actionPrefix) {
		app.handleAction(ctx, client, query, message.Chat.ID, message.ID, locale)
		return
	}
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

func (app *App) handleAction(ctx context.Context, client *telegram.Bot, query *models.CallbackQuery, chatID int64, messageID int, locale Locale) {
	parts := strings.Split(strings.TrimPrefix(query.Data, actionPrefix), ":")
	var view View
	var err error
	switch parts[0] {
	case "support":
		err = app.store.BeginSupport(ctx, query.From.ID)
		view = supportComposeView(locale)
	case "lang":
		if len(parts) != 2 {
			err = errors.New("missing locale")
			break
		}
		err = app.store.SetLocale(ctx, query.From.ID, parts[1])
		locale = app.locale(ctx, query.From.ID, query.From.LanguageCode)
		view = app.loadView(ctx, query.From.ID, locale, routeSettings)
	case "notify":
		if len(parts) != 2 {
			err = errors.New("missing notification kind")
			break
		}
		err = app.store.ToggleNotification(ctx, query.From.ID, parts[1])
		view = app.loadView(ctx, query.From.ID, locale, routeSettings)
	case "device-confirm":
		view = deviceDeleteConfirmView(locale, actionIndex(parts), false)
	case "devices-confirm":
		view = deviceDeleteConfirmView(locale, 0, true)
	case "device-delete":
		err = app.deleteDevice(ctx, query.From.ID, actionIndex(parts))
		view = app.loadView(ctx, query.From.ID, locale, routeDevices)
	case "devices-delete":
		var userID int64
		userID, err = app.store.RemnawaveUserID(ctx, query.From.ID)
		if err == nil {
			err = app.remnawave.DeleteAllDevices(ctx, userID)
		}
		view = app.loadView(ctx, query.From.ID, locale, routeDevices)
	case "revoke-confirm":
		view = revokeConfirmView(locale)
	case "revoke":
		var userID int64
		userID, err = app.store.RemnawaveUserID(ctx, query.From.ID)
		if err == nil {
			err = app.remnawave.RevokeSubscription(ctx, userID)
		}
		view = app.loadView(ctx, query.From.ID, locale, routeSubscription)
	default:
		err = errors.New("unknown action")
	}
	if err != nil {
		app.logger.Error("telegram action failed", "action", parts[0], "error", err)
		view = actionErrorView(locale)
	}
	if _, editErr := client.EditMessageText(ctx, editParams(chatID, messageID, view)); editErr != nil {
		app.logger.Error("telegram action view edit failed", "error", editErr)
	}
}

func (app *App) deleteDevice(ctx context.Context, telegramID int64, index int) error {
	userID, err := app.store.RemnawaveUserID(ctx, telegramID)
	if err != nil {
		return err
	}
	devices, err := app.remnawave.Devices(ctx, userID)
	if err != nil {
		return err
	}
	if index < 0 || index >= len(devices.Devices) {
		return errors.New("device list changed; refresh and try again")
	}
	return app.remnawave.DeleteDevice(ctx, userID, devices.Devices[index].HWID)
}

func actionIndex(parts []string) int {
	if len(parts) != 2 {
		return -1
	}
	value, err := strconv.Atoi(parts[1])
	if err != nil {
		return -1
	}
	return value
}

func (app *App) loadView(ctx context.Context, telegramID int64, locale Locale, route string) View {
	if route == routeSupport {
		return supportView(locale, app.supportURL)
	}
	userID, err := app.store.RemnawaveUserID(ctx, telegramID)
	if errors.Is(err, ErrNotLinked) {
		user, lookupErr := app.remnawave.UserByTelegramID(ctx, telegramID)
		if errors.Is(lookupErr, remnawave.ErrNotFound) {
			return notLinkedView(locale, app.supportURL)
		}
		if lookupErr != nil {
			app.logger.Error("Remnawave Telegram identity lookup failed", "error", lookupErr)
			return errorView(locale, route)
		}
		userID, err = app.store.Link(ctx, telegramID, user.ID)
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
	case routeSettings:
		preferences, preferenceErr := app.store.Preferences(ctx, telegramID)
		if preferenceErr != nil {
			return errorView(locale, route)
		}
		return settingsView(locale, preferences)
	case routeReferral:
		code, count, referralErr := app.store.Referral(ctx, telegramID)
		if referralErr != nil {
			return errorView(locale, route)
		}
		return referralView(locale, app.botUsername, code, count)
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
	case routeHome, routeSubscription, routeConnect, routeDevices, routeSupport, routeSettings, routeReferral:
		return true
	default:
		return false
	}
}

func (app *App) locale(ctx context.Context, telegramID int64, telegramLocale string) Locale {
	preferences, err := app.store.Preferences(ctx, telegramID)
	if err == nil {
		switch preferences.Locale {
		case "ru":
			return LocaleRussian
		case "en":
			return LocaleEnglish
		}
	}
	return localeFrom(telegramLocale)
}

func referralURL(username, code string) string {
	if username == "" || code == "" {
		return ""
	}
	return fmt.Sprintf("https://t.me/%s?start=%s", url.PathEscape(username), url.QueryEscape("ref_"+code))
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
