package app

import (
	"net/http"
	"net/url"
	"time"

	telegram "github.com/go-telegram/bot"
	"github.com/omniflow/omniflow/internal/config"
)

// TelegramOptions are the client options every process that talks to the Bot
// API applies: a self-hosted Bot API server in place of api.telegram.org, and
// a proxy — http(s) or SOCKS5 — for installations where Telegram is blocked.
// With neither set they are empty and the library's defaults apply.
func TelegramOptions(transport config.TelegramTransportConfig) []telegram.Option {
	var options []telegram.Option
	if transport.APIURL != "" {
		options = append(options, telegram.WithServerURL(transport.APIURL))
	}
	if transport.ProxyURL != "" {
		if proxy, err := url.Parse(transport.ProxyURL); err == nil {
			// The long-poll request waits up to a minute for updates, so the
			// client's own timeout sits above it, as the library's default does.
			client := &http.Client{
				Timeout:   telegramPollTimeout + 10*time.Second,
				Transport: &http.Transport{Proxy: http.ProxyURL(proxy), ForceAttemptHTTP2: true},
			}
			options = append(options, telegram.WithHTTPClient(telegramPollTimeout, client))
		}
	}
	return options
}

const telegramPollTimeout = time.Minute
