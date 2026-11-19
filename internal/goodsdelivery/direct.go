package goodsdelivery

import (
	"log/slog"
	"strings"

	"github.com/omniflow/omniflow/internal/config"
	"github.com/omniflow/omniflow/internal/goods/fragmentdirect"
	"github.com/omniflow/omniflow/internal/payments"
	"github.com/omniflow/omniflow/internal/tonrate"
)

// DirectOptionsFrom turns the environment into the direct adapter's options.
// Every process that resolves providers calls it the same way, so the api, the
// bot, and the worker cannot disagree about whether dry-run is on.
func DirectOptionsFrom(fragment config.FragmentConfig, currency string, logger *slog.Logger) DirectOptions {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	exponent, err := payments.CurrencyExponent(currency)
	if err != nil {
		exponent = 2
	}
	return DirectOptions{
		Config: fragmentdirect.Config{
			Cookies:       fragment.Cookies,
			Mnemonic:      fragment.WalletMnemonic,
			WalletVersion: fragment.WalletVersion,
			APIProvider:   fragment.TONAPIProvider,
			APIKey:        fragment.TONAPIKey,
			APIRPS:        fragment.TONAPIRPS,
			ProxyURL:      fragment.ProxyURL,
			ShowSender:    fragment.ShowSender,
			DryRun:        fragment.DryRun,
			Currency:      currency,
		},
		Rates: tonrate.New(tonrate.Options{
			Currency: currency, Exponent: exponent, ManualMinorPerTON: fragment.TONRateMinor,
		}),
		Logger: logger,
	}
}

// Rates is the exchange-rate service the direct adapter was configured with,
// shared so the panel and the worker quote one cache.
func (registry *Registry) Rates() *tonrate.Service {
	if registry == nil || registry.direct == nil {
		return nil
	}
	return registry.direct.Rates
}
