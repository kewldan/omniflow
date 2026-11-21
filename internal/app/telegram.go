package app

import (
	"net/http"
	"net/url"
	"path"
	"time"

	telegram "github.com/go-telegram/bot"
	"github.com/omniflow/omniflow/internal/config"
)

// TelegramObserver is told about every Bot API call except the long poll: the
// method name and a bounded outcome.
type TelegramObserver func(method, outcome string)

// TelegramOptions are the client options every process that talks to the Bot
// API applies: a self-hosted Bot API server in place of api.telegram.org, and
// a proxy — http(s) or SOCKS5 — for installations where Telegram is blocked.
// With neither set and no observer they are empty and the library's defaults
// apply. An observer wraps the transport, so every outbound call — a reply, a
// broadcast, an operator notice — is counted in one place.
func TelegramOptions(transport config.TelegramTransportConfig, observe ...TelegramObserver) []telegram.Option {
	var options []telegram.Option
	if transport.APIURL != "" {
		options = append(options, telegram.WithServerURL(transport.APIURL))
	}
	var roundTripper http.RoundTripper
	if transport.ProxyURL != "" {
		if proxy, err := url.Parse(transport.ProxyURL); err == nil {
			roundTripper = &http.Transport{Proxy: http.ProxyURL(proxy), ForceAttemptHTTP2: true}
		}
	}
	var observer TelegramObserver
	if len(observe) > 0 {
		observer = observe[0]
	}
	if observer != nil {
		if roundTripper == nil {
			roundTripper = http.DefaultTransport
		}
		roundTripper = observingTransport{next: roundTripper, observe: observer}
	}
	if roundTripper != nil {
		// The long-poll request waits up to a minute for updates, so the
		// client's own timeout sits above it, as the library's default does.
		client := &http.Client{Timeout: telegramPollTimeout + 10*time.Second, Transport: roundTripper}
		options = append(options, telegram.WithHTTPClient(telegramPollTimeout, client))
	}
	return options
}

const telegramPollTimeout = time.Minute

// observingTransport classifies each Bot API response by status code. The
// request path carries the bot token, so only its last segment — the method
// name — is ever looked at, and only when it is a plain identifier.
type observingTransport struct {
	next    http.RoundTripper
	observe TelegramObserver
}

func (transport observingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	method := TelegramMethod(request.URL.Path)
	response, err := transport.next.RoundTrip(request)
	if method == "getUpdates" {
		return response, err
	}
	switch {
	case err != nil:
		transport.observe(method, "unavailable")
	default:
		transport.observe(method, TelegramOutcome(response.StatusCode))
	}
	return response, err
}

// TelegramMethod extracts the Bot API method from a request path, or "other"
// when the last segment is not a short alphabetic identifier.
func TelegramMethod(requestPath string) string {
	method := path.Base(requestPath)
	if len(method) == 0 || len(method) > 40 {
		return "other"
	}
	for _, r := range method {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return "other"
		}
	}
	return method
}

// TelegramOutcome buckets a Bot API status code.
func TelegramOutcome(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "ok"
	case status == http.StatusForbidden:
		return "blocked"
	case status == http.StatusTooManyRequests:
		return "rate_limited"
	case status == http.StatusBadRequest:
		return "rejected"
	case status == http.StatusNotFound:
		return "not_found"
	default:
		return "telegram_error"
	}
}
