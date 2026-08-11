package customerauthpg

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// telegramBotUsername caches the bot's own @name.
//
// The login widget is loaded by username, not by token, and the token is the
// only thing this process is configured with. Rather than adding a second
// environment variable an operator has to keep in step with the first, the
// username is resolved once from the bot API and remembered.
type telegramBotUsername struct {
	once     sync.Once
	username string
}

// BotUsername returns the bot's username for the login widget, or the empty
// string when it could not be resolved.
//
// A failure is not an error the caller has to handle: it means the widget cannot
// be offered, and the sign-in screen simply does not show that button. Resolving
// lazily rather than at startup keeps a Telegram outage from delaying the API
// coming up, and the result is cached for the process lifetime because a bot's
// username changes about as often as its token does.
func (service *Service) BotUsername(ctx context.Context) string {
	if service.botToken == "" {
		return ""
	}
	service.botName.once.Do(func() {
		requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		request, err := http.NewRequestWithContext(
			requestCtx, http.MethodGet,
			"https://api.telegram.org/bot"+service.botToken+"/getMe", nil,
		)
		if err != nil {
			return
		}
		response, err := service.httpClient.Do(request)
		if err != nil {
			return
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusOK {
			return
		}

		var payload struct {
			OK     bool `json:"ok"`
			Result struct {
				Username string `json:"username"`
			} `json:"result"`
		}
		if err = json.NewDecoder(response.Body).Decode(&payload); err != nil || !payload.OK {
			return
		}
		service.botName.username = strings.TrimPrefix(strings.TrimSpace(payload.Result.Username), "@")
	})
	return service.botName.username
}
