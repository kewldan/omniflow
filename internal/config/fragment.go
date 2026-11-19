package config

import (
	"os"
	"strconv"
	"strings"
)

// FragmentConfig configures the direct Fragment goods provider
// (`fragment_direct`). Every secret in it — the Fragment session cookies, the
// paying wallet's mnemonic, and the TON API key — is read from the
// environment only: it is never stored in the database, never returned by an
// API, and never logged. The rest are environment variables too, so one .env
// configures the api, bot, and worker identically.
type FragmentConfig struct {
	// Cookies is APP_FRAGMENT_COOKIES: stel_ssid, stel_dt, stel_token, and
	// stel_ton_token of a KYC-verified Fragment account, as `k=v; …` or JSON.
	Cookies string
	// WalletMnemonic is APP_FRAGMENT_WALLET_MNEMONIC, the paying wallet's
	// 24-word seed phrase.
	WalletMnemonic string
	// WalletVersion is APP_FRAGMENT_WALLET_VERSION: V5R1 (default) or V4R2.
	WalletVersion string
	// TONAPIProvider is APP_FRAGMENT_TON_API_PROVIDER: toncenter (default) or
	// tonapi, and TONAPIKey its key (APP_FRAGMENT_TON_API_KEY).
	TONAPIProvider string
	TONAPIKey      string
	// TONAPIRPS is APP_FRAGMENT_TON_API_RPS, requests per second to the TON
	// API from one process. It defaults to 1, the free tier's limit.
	TONAPIRPS float64
	// ProxyURL is APP_FRAGMENT_PROXY_URL, an optional proxy for fragment.com.
	ProxyURL string
	// ShowSender is APP_FRAGMENT_SHOW_SENDER: show the operator's Fragment
	// account as the sender of the Stars. Off by default.
	ShowSender bool
	// DryRun is APP_FRAGMENT_DRY_RUN: deliveries pass through every state
	// without contacting Fragment or the wallet. For staging.
	DryRun bool
	// TONRateMinor is APP_FRAGMENT_TON_RATE_MINOR: a fixed price of one TON in
	// minor units of the shop currency. Zero reads the rate from tonapi.io,
	// falling back to CoinGecko.
	TONRateMinor int64
	// WalletLowStars is APP_FRAGMENT_WALLET_LOW_STARS: a sale notice asks for a
	// top-up when the wallet covers fewer Stars than this. Default 1000.
	WalletLowStars int
	// VPNOffer is APP_GOODS_VPN_OFFER_ENABLED: after a delivery, a buyer with
	// no active paid subscription is offered VPN — a trial if they never had
	// one, a purchase otherwise. On by default.
	VPNOffer bool
}

// Configured reports whether every secret a real purchase needs is present.
func (cfg FragmentConfig) Configured() bool {
	return cfg.Cookies != "" && cfg.WalletMnemonic != "" && cfg.TONAPIKey != ""
}

// LoadFragment reads the direct Fragment provider's environment. It never
// fails: a missing secret leaves the provider unconfigured, which the panel
// reports, rather than stopping a process that may never sell Stars.
func LoadFragment() FragmentConfig {
	rps := 1.0
	if raw := strings.TrimSpace(os.Getenv("APP_FRAGMENT_TON_API_RPS")); raw != "" {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil && parsed >= 0 {
			rps = parsed
		}
	}
	return FragmentConfig{
		Cookies:        strings.TrimSpace(os.Getenv("APP_FRAGMENT_COOKIES")),
		WalletMnemonic: strings.TrimSpace(os.Getenv("APP_FRAGMENT_WALLET_MNEMONIC")),
		WalletVersion:  envOr("APP_FRAGMENT_WALLET_VERSION", "V5R1"),
		TONAPIProvider: envOr("APP_FRAGMENT_TON_API_PROVIDER", "toncenter"),
		TONAPIKey:      strings.TrimSpace(os.Getenv("APP_FRAGMENT_TON_API_KEY")),
		TONAPIRPS:      rps,
		ProxyURL:       strings.TrimSpace(os.Getenv("APP_FRAGMENT_PROXY_URL")),
		ShowSender:     boolEnvOr("APP_FRAGMENT_SHOW_SENDER", false),
		DryRun:         boolEnvOr("APP_FRAGMENT_DRY_RUN", false),
		TONRateMinor:   max(int64EnvOr("APP_FRAGMENT_TON_RATE_MINOR", 0), 0),
		WalletLowStars: intEnvOr("APP_FRAGMENT_WALLET_LOW_STARS", 1000),
		VPNOffer:       boolEnvOr("APP_GOODS_VPN_OFFER_ENABLED", true),
	}
}
