package paymentservice

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/payments"
)

// optionSettingsTTL is how long the operator's button configuration is reused
// before it is read again. A save in the panel calls InvalidateOptions in its
// own process; the others pick it up within this window.
const optionSettingsTTL = 30 * time.Second

// optionSettings is the operator's configuration of the customer-facing
// payment buttons: the provider switch and order from
// payment_provider_settings, and per-method presentation from
// payment_method_settings.
type optionSettings struct {
	providers map[string]dbgen.PaymentProviderSetting
	methods   map[string]dbgen.PaymentMethodSetting
}

type optionCache struct {
	mutex    sync.Mutex
	loadedAt time.Time
	settings optionSettings
}

// InvalidateOptions drops the cached button configuration so the next list
// reads what the operator just saved.
func (service *Service) InvalidateOptions() {
	service.options.mutex.Lock()
	service.options.loadedAt = time.Time{}
	service.options.mutex.Unlock()
}

// optionSettings reads the configuration, reusing it for a short while. A
// read that fails keeps the previous configuration: a database hiccup must
// not make every payment button disappear, nor reappear a disabled one that
// was already hidden.
func (service *Service) optionSettings() optionSettings {
	cache := &service.options
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	if service.pool == nil || (!cache.loadedAt.IsZero() && service.clock().Sub(cache.loadedAt) < optionSettingsTTL) {
		return cache.settings
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	queries := dbgen.New(service.pool)
	providers, err := queries.ListPaymentProviderSettings(ctx)
	if err != nil {
		return cache.settings
	}
	methods, err := queries.ListPaymentMethodSettings(ctx)
	if err != nil {
		return cache.settings
	}
	settings := optionSettings{
		providers: make(map[string]dbgen.PaymentProviderSetting, len(providers)),
		methods:   make(map[string]dbgen.PaymentMethodSetting, len(methods)),
	}
	for _, row := range providers {
		// The single-merchant row is the one that drives the customer list.
		if row.MerchantID == "" {
			settings.providers[row.Provider] = row
		}
	}
	for _, row := range methods {
		settings.methods[payments.OptionKey(row.Provider, row.Method)] = row
	}
	cache.settings, cache.loadedAt = settings, service.clock()
	return settings
}

// Options lists the customer-facing payment buttons: one per configured
// adapter, or one per method for an adapter configured with several.
//
// The operator's panel settings decide what is shown and in what order. A
// provider the operator switched off is left out entirely; a provider with no
// saved settings is shown, because an adapter whose credentials are in the
// environment has always been offered. Per-method rows hide a single button,
// rename it, and carry the rules the customer surfaces apply (amount bounds,
// quick amounts, who may see it).
func (service *Service) Options() []commerce.PaymentOption {
	settings := service.optionSettings()
	options := make([]commerce.PaymentOption, 0, len(service.providers))
	for name, provider := range service.providers {
		order := providerDisplayOrder[name] * 100
		if stored, ok := settings.providers[name]; ok {
			if !stored.Enabled {
				continue
			}
			order = int(stored.DisplayOrder) * 100
		}
		var currencies []string
		if support, ok := provider.(payments.CurrencySupport); ok {
			currencies = supportedCurrencies(support)
		}
		for index, method := range payments.ProviderMethods(provider) {
			key := payments.OptionKey(name, method)
			option := commerce.PaymentOption{
				Provider: name, Method: method, Key: key, Enabled: true, Currencies: currencies,
				Recurring: provider.Capabilities().Recurring, Order: order + index,
			}
			if stored, ok := settings.methods[key]; ok {
				if !stored.Enabled {
					continue
				}
				option.Order = order + int(stored.DisplayOrder)
				option.Presentation = presentationFrom(stored)
			}
			options = append(options, option)
		}
	}
	sort.SliceStable(options, func(left, right int) bool {
		if options[left].Order != options[right].Order {
			return options[left].Order < options[right].Order
		}
		return options[left].Key < options[right].Key
	})
	return options
}

// Option finds one button by its key.
func (service *Service) Option(key string) (commerce.PaymentOption, bool) {
	for _, option := range service.Options() {
		if option.Key == key {
			return option, true
		}
	}
	return commerce.PaymentOption{}, false
}

// Audience reads what a customer's payment buttons are filtered by.
func (service *Service) Audience(ctx context.Context, customerID string) (commerce.PaymentAudience, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return commerce.PaymentAudience{}, err
	}
	row, err := dbgen.New(service.pool).CustomerPaymentAudience(ctx, id)
	if err != nil {
		return commerce.PaymentAudience{}, err
	}
	return commerce.PaymentAudience{LoginKinds: row.LoginKinds, ToppedUp: row.ToppedUp, TierCode: row.TierCode}, nil
}

func presentationFrom(row dbgen.PaymentMethodSetting) commerce.MethodPresentation {
	return commerce.MethodPresentation{
		NameRU: row.NameRu, NameEN: row.NameEn,
		DescriptionRU: row.DescriptionRu, DescriptionEN: row.DescriptionEn,
		MinMinor: row.MinMinor.Int64, MaxMinor: row.MaxMinor.Int64,
		QuickAmountsMinor: row.QuickAmountsMinor, LoginKinds: row.LoginKinds,
		TopUpAudience: row.TopupAudience, LoyaltyTierCodes: row.LoyaltyTierCodes,
		OpenDirectly: row.OpenDirectly,
	}
}

// supportedCurrencies enumerates what an adapter settles from the closed set
// Omniflow prices in.
func supportedCurrencies(support payments.CurrencySupport) []string {
	supported := make([]string, 0, 4)
	for _, currency := range payments.SettlementCurrencies() {
		if support.SupportsCurrency(currency) {
			supported = append(supported, currency)
		}
	}
	return supported
}
