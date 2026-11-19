package botapp

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// MenuButton is the button Telegram shows beside the message field.
type MenuButton struct {
	// MiniApp replaces the commands list with a button that opens the
	// customer panel as a Mini App (APP_BOT_MENU_BUTTON_MINIAPP).
	MiniApp bool
	// Text is the button's label (APP_BOT_MENU_BUTTON_TEXT).
	Text string
	// URL is the panel's address the button opens.
	URL string
}

// MenuButtonFromEnv reads the menu button settings. The button opens
// APP_PUBLIC_URL's /account, and only an https address can be one: Telegram
// refuses anything else for a Mini App.
func MenuButtonFromEnv(getenv func(string) string, publicURL string) MenuButton {
	enabled, _ := strconv.ParseBool(strings.TrimSpace(getenv("APP_BOT_MENU_BUTTON_MINIAPP")))
	label := strings.TrimSpace(getenv("APP_BOT_MENU_BUTTON_TEXT"))
	if label == "" {
		label = "Кабинет"
	}
	base := strings.TrimRight(strings.TrimSpace(publicURL), "/")
	if !strings.HasPrefix(strings.ToLower(base), "https://") {
		enabled = false
	}
	return MenuButton{MiniApp: enabled, Text: label, URL: base + "/account"}
}

// ConfigureMenuButton sets the bot-wide menu button. A disabled setting
// leaves whatever button the bot already has, so an operator who set one by
// hand in BotFather is not overruled.
func ConfigureMenuButton(ctx context.Context, logger *slog.Logger, client *telegram.Bot, button MenuButton) {
	if !button.MiniApp || client == nil {
		return
	}
	_, err := client.SetChatMenuButton(ctx, &telegram.SetChatMenuButtonParams{
		MenuButton: models.MenuButtonWebApp{
			Type: models.MenuButtonTypeWebApp, Text: button.Text,
			WebApp: models.WebAppInfo{URL: button.URL},
		},
	})
	if err != nil {
		logger.Warn("telegram menu button setup failed", "error", err)
	}
}
