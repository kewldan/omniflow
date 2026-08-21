package customerauthpg

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// botUsernameRetryAfter is how long a failed getMe call is remembered before
// the next sign-in screen tries again.
//
// It is short on purpose. The cost of retrying is one outbound request per
// sign-in screen; the cost of not retrying is a sign-in screen that hides the
// Telegram button for as long as the process lives, because the bot API
// happened to time out once at startup.
const botUsernameRetryAfter = 30 * time.Second

// telegramBotUsername caches the bot's own @name.
//
// The login widget is loaded by username, not by token, and the token is the
// only thing this process is configured with. Rather than adding a second
// environment variable an operator has to keep in step with the first, the
// username is resolved from the bot API and remembered.
//
// Only a successful answer is kept for the life of the process. A failure is
// remembered just long enough to keep a Telegram outage from being amplified
// into one getMe call per page view, and is then retried, so the sign-in
// screen recovers on its own once the bot API does.
type telegramBotUsername struct {
	mu         sync.Mutex
	username   string
	failedAt   time.Time
	retryAfter time.Duration
}

// BotUsername returns the bot's username for the login widget, or the empty
// string when it could not be resolved.
//
// A failure is not an error the caller has to handle: it means the widget cannot
// be offered right now, and the sign-in screen simply does not show that
// button. Resolving lazily rather than at startup keeps a Telegram outage from
// delaying the API coming up.
func (service *Service) BotUsername(ctx context.Context) string {
	if service.botToken == "" {
		return ""
	}
	cache := &service.botName
	cache.mu.Lock()
	defer cache.mu.Unlock()

	if cache.username != "" {
		return cache.username
	}
	retryAfter := cache.retryAfter
	if retryAfter <= 0 {
		retryAfter = botUsernameRetryAfter
	}
	if !cache.failedAt.IsZero() && service.now().Sub(cache.failedAt) < retryAfter {
		return ""
	}

	username, ok := service.fetchBotUsername(ctx)
	if !ok {
		cache.failedAt = service.now()
		return ""
	}
	cache.username = username
	cache.failedAt = time.Time{}
	return username
}

// fetchBotUsername asks the bot API once. It reports false for any failure,
// including a well-formed answer with no username in it.
func (service *Service) fetchBotUsername(ctx context.Context) (string, bool) {
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	request, err := http.NewRequestWithContext(
		requestCtx, http.MethodGet,
		service.telegramAPIBase()+"/bot"+service.botToken+"/getMe", nil,
	)
	if err != nil {
		return "", false
	}
	response, err := service.httpClient.Do(request)
	if err != nil {
		return "", false
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", false
	}

	var payload struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if err = json.NewDecoder(response.Body).Decode(&payload); err != nil || !payload.OK {
		return "", false
	}
	username := strings.TrimPrefix(strings.TrimSpace(payload.Result.Username), "@")
	return username, username != ""
}

// telegramAPIBase is the bot API origin. Tests point it at a local server; a
// running installation always talks to Telegram.
func (service *Service) telegramAPIBase() string {
	if service.telegramAPI != "" {
		return strings.TrimRight(service.telegramAPI, "/")
	}
	return "https://api.telegram.org"
}
