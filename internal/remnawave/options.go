package remnawave

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Auth modes for a panel that sits behind something more than its own token
// check. Every mode still sends the API token as `Authorization: Bearer`,
// because that is what the panel itself verifies; the modes add what the
// reverse proxy in front of it wants.
const (
	// AuthBearer sends the token alone. It is the default.
	AuthBearer = "bearer"
	// AuthAPIKey also repeats the token as `X-Api-Key`, which is what a
	// Bedolaga installation sent by default and what some proxies check.
	AuthAPIKey = "api_key"
	// AuthBasic adds `X-Api-Key: Basic base64(user:password)` for a proxy that
	// guards the panel with basic credentials, the way Bedolaga sends them.
	AuthBasic = "basic"
	// AuthCaddy adds `X-Api-Key: <caddy token>` for remnawave/caddy-with-auth.
	AuthCaddy = "caddy"
	// AuthCookies sends the token and the configured cookies only.
	AuthCookies = "cookies"
)

// Options are the transport settings beyond the URL and token.
type Options struct {
	// AuthType is one of the Auth* constants; empty means AuthBearer.
	AuthType string
	// Username and Password are the proxy credentials for AuthBasic.
	Username string
	Password string
	// CaddyToken is the proxy token for AuthCaddy.
	CaddyToken string
	// SecretKey is the cookie a panel installed with a secret URL key
	// expects, as `name:value`. A value without a colon is sent as
	// `value=value`, which is how the panel's installer names it.
	SecretKey string
	// Cookies are extra cookies, `name=value; other=value`.
	Cookies string
	// Timeout bounds one call. Zero keeps the ten-second default.
	Timeout time.Duration
	// RequestsPerMinute caps the calls this process makes. Zero is no cap.
	RequestsPerMinute int
}

// Validate reports a setting that cannot work, so a typo fails startup rather
// than every call.
func (options Options) Validate() error {
	switch strings.ToLower(strings.TrimSpace(options.AuthType)) {
	case "", AuthBearer, AuthAPIKey, AuthCookies:
	case AuthBasic:
		if options.Username == "" || options.Password == "" {
			return errors.New("APP_REMNAWAVE_AUTH_TYPE=basic needs APP_REMNAWAVE_USERNAME and APP_REMNAWAVE_PASSWORD")
		}
	case AuthCaddy:
		if strings.TrimSpace(options.CaddyToken) == "" {
			return errors.New("APP_REMNAWAVE_AUTH_TYPE=caddy needs APP_REMNAWAVE_CADDY_TOKEN")
		}
	default:
		return fmt.Errorf("APP_REMNAWAVE_AUTH_TYPE %q is not one of bearer, api_key, basic, caddy, cookies", options.AuthType)
	}
	if options.Timeout < 0 || options.RequestsPerMinute < 0 {
		return errors.New("Remnawave timeout and request rate cannot be negative")
	}
	for _, cookie := range parseCookies(options.Cookies) {
		if cookie.Name == "" {
			return errors.New("APP_REMNAWAVE_COOKIES must be name=value pairs separated by semicolons")
		}
	}
	return nil
}

// OptionsFromEnv reads APP_REMNAWAVE_AUTH_TYPE and its companions through the
// getter it is given, so the adapter does not reach into the environment
// itself.
func OptionsFromEnv(getenv func(string) string) Options {
	number := func(key string) int {
		value, err := strconv.Atoi(strings.TrimSpace(getenv(key)))
		if err != nil || value < 0 {
			return 0
		}
		return value
	}
	return Options{
		AuthType:          strings.ToLower(strings.TrimSpace(getenv("APP_REMNAWAVE_AUTH_TYPE"))),
		Username:          getenv("APP_REMNAWAVE_USERNAME"),
		Password:          getenv("APP_REMNAWAVE_PASSWORD"),
		CaddyToken:        strings.TrimSpace(getenv("APP_REMNAWAVE_CADDY_TOKEN")),
		SecretKey:         strings.TrimSpace(getenv("APP_REMNAWAVE_SECRET_KEY")),
		Cookies:           strings.TrimSpace(getenv("APP_REMNAWAVE_COOKIES")),
		Timeout:           time.Duration(number("APP_REMNAWAVE_TIMEOUT_SECONDS")) * time.Second,
		RequestsPerMinute: number("APP_REMNAWAVE_REQUESTS_PER_MINUTE"),
	}
}

// authenticate puts the configured credentials on one request.
func (options Options) authenticate(request *http.Request, token string) {
	request.Header.Set("Authorization", "Bearer "+token)
	switch strings.ToLower(strings.TrimSpace(options.AuthType)) {
	case AuthAPIKey:
		request.Header.Set("X-Api-Key", token)
	case AuthBasic:
		credentials := base64.StdEncoding.EncodeToString([]byte(options.Username + ":" + options.Password))
		request.Header.Set("X-Api-Key", "Basic "+credentials)
	case AuthCaddy:
		request.Header.Set("X-Api-Key", options.CaddyToken)
	}
	if options.SecretKey != "" {
		name, value, found := strings.Cut(options.SecretKey, ":")
		if !found {
			value = name
		}
		request.AddCookie(&http.Cookie{Name: strings.TrimSpace(name), Value: strings.TrimSpace(value)})
	}
	for _, cookie := range parseCookies(options.Cookies) {
		request.AddCookie(cookie)
	}
}

func parseCookies(raw string) []*http.Cookie {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var cookies []*http.Cookie
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, _ := strings.Cut(part, "=")
		cookies = append(cookies, &http.Cookie{Name: strings.TrimSpace(name), Value: strings.TrimSpace(value)})
	}
	return cookies
}

// limiter spaces calls evenly so a process never exceeds its per-minute
// budget. It is a simple interval gate rather than a token bucket: a panel
// that asks for a request rate wants the calls spread out, not bursts.
type limiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func newLimiter(perMinute int) *limiter {
	if perMinute <= 0 {
		return nil
	}
	return &limiter{interval: time.Minute / time.Duration(perMinute)}
}

func (gate *limiter) wait(ctx context.Context) error {
	if gate == nil {
		return nil
	}
	gate.mu.Lock()
	now := time.Now()
	slot := gate.next
	if slot.Before(now) {
		slot = now
	}
	gate.next = slot.Add(gate.interval)
	gate.mu.Unlock()
	delay := time.Until(slot)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
