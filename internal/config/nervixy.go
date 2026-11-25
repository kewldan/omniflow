package config

import (
	"os"
	"strings"
)

// NervixyConfig configures the Nervixy Steam top-up provider (`nervixy`).
// Its API key spends the operator's prepaid Nervixy balance, so it is read
// from the environment only and never stored, returned, or logged. Without it
// the provider does not exist as far as Omniflow is concerned.
type NervixyConfig struct {
	// APIKey is APP_NERVIXY_API_KEY, issued in the Nervixy account's API
	// settings.
	APIKey string
	// BaseURL is APP_NERVIXY_BASE_URL, the api.php endpoint every action is
	// sent to.
	BaseURL string
}

// NervixyProductionURL is the endpoint Nervixy documents.
const NervixyProductionURL = "https://nervixy.digital/api.php"

// Configured reports whether the provider can be used at all.
func (cfg NervixyConfig) Configured() bool { return cfg.APIKey != "" }

// LoadNervixy reads the Nervixy environment. It never fails.
func LoadNervixy() NervixyConfig {
	return NervixyConfig{
		APIKey:  strings.TrimSpace(os.Getenv("APP_NERVIXY_API_KEY")),
		BaseURL: strings.TrimSpace(envOr("APP_NERVIXY_BASE_URL", NervixyProductionURL)),
	}
}
