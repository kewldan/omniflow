package payments

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

// Env reads one configuration value. It is os.Getenv in production and a map
// in tests, so an adapter's builder can be exercised without touching the
// process environment.
type Env func(string) string

// Settings is what a builder needs to construct an adapter: the environment it
// reads its own credentials from, and the public origin providers call back to.
type Settings struct {
	Env Env
	// PublicURL is APP_PUBLIC_URL without a trailing slash. Providers that take
	// a callback URL per payment receive WebhookURL(slug) built from it.
	PublicURL string
}

// String reads a trimmed value.
func (settings Settings) String(key string) string {
	if settings.Env == nil {
		return strings.TrimSpace(os.Getenv(key))
	}
	return strings.TrimSpace(settings.Env(key))
}

// Bool reads a boolean value, falling back when it is empty or malformed.
func (settings Settings) Bool(key string, fallback bool) bool {
	value, err := strconv.ParseBool(settings.String(key))
	if err != nil {
		return fallback
	}
	return value
}

// Int reads an integer value, falling back when it is empty or malformed.
func (settings Settings) Int(key string, fallback int) int {
	value, err := strconv.Atoi(settings.String(key))
	if err != nil {
		return fallback
	}
	return value
}

// WebhookURL is the public route a provider posts settlement notifications to.
// It is empty when the installation has no public URL, which an adapter that
// must send a callback URL treats as a configuration error.
func (settings Settings) WebhookURL(slug string) string {
	base := strings.TrimSuffix(strings.TrimSpace(settings.PublicURL), "/")
	if base == "" {
		return ""
	}
	return base + "/v1/payments/webhooks/" + slug
}

// ErrNotConfigured is returned by a builder whose credentials are absent. It
// is not a failure: the adapter is simply left unregistered.
var ErrNotConfigured = errors.New("payment provider is not configured")

// Builder constructs one adapter from the installation's settings.
//
// A builder returns ErrNotConfigured when none of its credentials are set, and
// any other error when they are set but incomplete or invalid — an operator
// who configured half a provider should hear about it at startup, not on the
// first checkout.
type Builder struct {
	Slug  string
	Build func(Settings) (Provider, error)
}

// Builders is the explicit list of environment-configured adapters, beyond the
// always-present manual and Telegram Stars rails and the two with their own
// typed options (CryptoBot and YooKassa). Order here does not affect what a
// customer sees: the customer-facing list is sorted by slug.
var Builders = []Builder{
	{Slug: "heleket", Build: buildHeleket},
	{Slug: "mulenpay", Build: buildMulenPay},
	{Slug: "pal24", Build: buildPal24},
	{Slug: "platega", Build: buildPlatega},
	{Slug: "cashera", Build: buildCashera},
	{Slug: "tribute", Build: buildTribute},
	{Slug: "freekassa", Build: buildFreeKassa},
	{Slug: "kassa_ai", Build: buildKassaAI},
	{Slug: "riopay", Build: buildRioPay},
	{Slug: "severpay", Build: buildSeverPay},
	{Slug: "rollypay", Build: buildRollyPay},
	{Slug: "aurapay", Build: buildAuraPay},
	{Slug: "wata", Build: buildWata},
	{Slug: "cloudpayments", Build: buildCloudPayments},
	{Slug: "lava", Build: buildLava},
	{Slug: "overpay", Build: buildOverpay},
	{Slug: "paritypay", Build: buildParityPay},
	{Slug: "payhot", Build: buildPayHot},
	{Slug: "cispay", Build: buildCisPay},
	{Slug: "antilopay", Build: buildAntilopay},
	{Slug: "anore", Build: buildAnore},
	{Slug: "donut", Build: buildDonut},
	{Slug: "etoplatezhi", Build: buildEtoPlatezhi},
	{Slug: "jupiter", Build: buildJupiter},
	{Slug: "tabpay", Build: buildTabPay},
}

// Slugs lists every adapter slug Omniflow can register. It matches the
// database check constraints on provider columns and the OpenAPI enum.
func Slugs() []string {
	slugs := []string{"manual", "telegram_stars", "cryptobot", "yookassa"}
	for _, builder := range Builders {
		slugs = append(slugs, builder.Slug)
	}
	return slugs
}

// WebhookAcknowledger is implemented by adapters whose provider expects a
// specific success body — FreeKassa wants the bare word YES, CloudPayments
// wants {"code":0} — rather than Omniflow's own JSON status.
//
// It is consulted only after a notification was verified and recorded. A
// rejected notification is always answered with a problem response, so a
// provider keeps retrying a delivery Omniflow could not apply.
type WebhookAcknowledger interface {
	WebhookAck(classification string) (status int, contentType string, body []byte)
}

// PollVerifiedWebhooks is implemented by adapters whose notifications carry no
// signature Omniflow can check. The payment service treats their body as a
// hint: it fetches the referenced payment through the authenticated API and
// applies only that answer.
type PollVerifiedWebhooks interface {
	WebhooksNeedPolling() bool
}

// NeedsWebhookPolling reports whether a notification from this adapter must be
// confirmed by Poll before it can move money.
func NeedsWebhookPolling(provider Provider) bool {
	if marker, ok := provider.(PollVerifiedWebhooks); ok {
		return marker.WebhooksNeedPolling()
	}
	return false
}
