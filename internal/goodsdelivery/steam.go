package goodsdelivery

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/omniflow/omniflow/internal/config"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/goods"
	"github.com/omniflow/omniflow/internal/goods/nervixy"
	"github.com/omniflow/omniflow/internal/goods/playwallet"
)

// SteamProviders are the providers that sell Steam top-ups, in the order the
// panel offers them. Each exists only while its key is in the environment.
var SteamProviders = []string{playwallet.Slug, nervixy.Slug}

// SellsSteam reports whether a provider slug sells Steam top-ups.
func SellsSteam(slug string) bool {
	for _, candidate := range SteamProviders {
		if candidate == slug {
			return true
		}
	}
	return false
}

// envProvider holds an adapter built once from the environment: its key
// cannot change without a restart, and an adapter may cache between quotes.
type envProvider struct {
	configured bool
	build      func() (goods.Provider, error)

	once sync.Once
	impl goods.Provider
	err  error
}

func (state *envProvider) get() (goods.Provider, error) {
	if state == nil || !state.configured {
		return nil, ErrProviderUnconfigured
	}
	state.once.Do(func() { state.impl, state.err = state.build() })
	if state.err != nil {
		return nil, fmt.Errorf("%w: %w", ErrProviderUnconfigured, state.err)
	}
	return state.impl, nil
}

// WithSteamProviders enables the Steam top-up adapters whose keys are in the
// environment. Without a key a provider stays off: it resolves as
// unconfigured and EnsureSteamProvidersListed adds nothing to the panel.
func (registry *Registry) WithSteamProviders(
	pay config.PlayWalletConfig, nx config.NervixyConfig, rates goods.DollarRate,
) *Registry {
	registry.steam = map[string]*envProvider{
		playwallet.Slug: {configured: pay.Configured(), build: func() (goods.Provider, error) {
			return playwallet.New(playwallet.Config{
				BaseURL: pay.BaseURL, APIKey: pay.APIKey, ServiceID: pay.ServiceID,
			}, rates)
		}},
		nervixy.Slug: {configured: nx.Configured(), build: func() (goods.Provider, error) {
			return nervixy.New(nervixy.Config{BaseURL: nx.BaseURL, APIKey: nx.APIKey}, rates)
		}},
	}
	return registry
}

// steamProvider resolves an environment-configured Steam adapter.
func (registry *Registry) steamProvider(slug string) (goods.Provider, error) {
	if registry == nil {
		return nil, ErrProviderUnconfigured
	}
	return registry.steam[slug].get()
}

// EnsureSteamProvidersListed adds each Steam provider whose key is in the
// environment to the panel's provider list, switched off. An installation
// without the keys never sees them, which is how the feature stays invisible
// until an operator chooses to sell Steam top-ups.
func (registry *Registry) EnsureSteamProvidersListed(ctx context.Context) error {
	if registry == nil {
		return nil
	}
	for _, slug := range SteamProviders {
		if state := registry.steam[slug]; state == nil || !state.configured {
			continue
		}
		if _, err := registry.pool.Exec(ctx,
			`INSERT INTO goods_providers (slug, enabled) VALUES ($1, false) ON CONFLICT (slug) DO NOTHING`,
			slug); err != nil {
			return err
		}
	}
	return nil
}

// resumesAfterMarker names the adapters whose settlement marker identifies an
// order the provider can be asked about, and paid again, without risk of
// buying twice. A delivery interrupted after such a marker is polled rather
// than parked for review.
func resumesAfterMarker(slug string) bool { return slug == playwallet.Slug }

// errNoResumableReference is the reason a marked delivery of a resumable
// adapter still parks: the marker carries no provider order to ask about.
var errNoResumableReference = errors.New("marked delivery has no provider order to resume")

// resumeReference is the provider order a marked delivery resumes from.
func resumeReference(delivery dbgen.GoodsDelivery) (string, error) {
	if delivery.ProviderReference.Valid && delivery.ProviderReference.String != "" {
		return delivery.ProviderReference.String, nil
	}
	if delivery.ProviderRequestID.Valid && delivery.ProviderRequestID.String != "" {
		return delivery.ProviderRequestID.String, nil
	}
	return "", errNoResumableReference
}
