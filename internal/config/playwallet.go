package config

import (
	"os"
	"strings"
)

// PlayWalletConfig configures the PlayWallet Steam top-up provider
// (`playwallet`). Its API key spends the operator's prepaid PlayWallet
// balance, so like every payment secret it is read from the environment only:
// it is never stored in the database, never returned by an API, and never
// logged. Without the key the provider does not exist as far as Omniflow is
// concerned — the panel does not list it and the bot offers no Steam top-up.
type PlayWalletConfig struct {
	// APIKey is APP_PLAYWALLET_API_KEY, the merchant key PlayWallet's
	// @pwMerchantBot issues with /tokens. The DEV and PROD keys differ.
	APIKey string
	// BaseURL is APP_PLAYWALLET_BASE_URL. It defaults to the production
	// merchant API; point it at https://dev.merchant.playwallet.bot/api/merchant
	// with the DEV key to test without spending.
	BaseURL string
	// ServiceID is APP_PLAYWALLET_SERVICE_ID, the PlayWallet service that
	// tops up Steam. Empty picks the only service the merchant has, or the one
	// whose name mentions Steam.
	ServiceID string
}

// PlayWalletProductionURL is the merchant API PlayWallet documents for live
// orders.
const PlayWalletProductionURL = "https://merchant.playwallet.bot/api/merchant"

// Configured reports whether the provider can be used at all.
func (cfg PlayWalletConfig) Configured() bool { return cfg.APIKey != "" }

// LoadPlayWallet reads the PlayWallet environment. It never fails: a missing
// key leaves the provider off rather than stopping a process that may never
// sell a Steam top-up.
func LoadPlayWallet() PlayWalletConfig {
	return PlayWalletConfig{
		APIKey:    strings.TrimSpace(os.Getenv("APP_PLAYWALLET_API_KEY")),
		BaseURL:   strings.TrimRight(envOr("APP_PLAYWALLET_BASE_URL", PlayWalletProductionURL), "/"),
		ServiceID: strings.TrimSpace(os.Getenv("APP_PLAYWALLET_SERVICE_ID")),
	}
}
