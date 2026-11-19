package goodsdelivery

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/goods"
	"github.com/omniflow/omniflow/internal/goods/fragmentdirect"
	"github.com/omniflow/omniflow/internal/tonrate"
)

// Errors a resolver returns for a provider that cannot be used right now. They
// are distinct from a delivery failure: nothing has been submitted.
var (
	// ErrProviderDisabled reports a provider an operator has switched off.
	ErrProviderDisabled = errors.New("digital goods provider is disabled")
	// ErrProviderUnconfigured reports one with no stored credential.
	ErrProviderUnconfigured = errors.New("digital goods provider has no credential")
	// ErrProviderUnknown reports a slug with no implementation compiled in.
	ErrProviderUnknown = errors.New("digital goods provider is not implemented")
)

// credentialLabel binds a sealed goods credential to its own column, so a
// ciphertext moved from another sealed field cannot be opened here. It matches
// the label `internal/panelpg` seals with.
const credentialLabel = "panel.goods_provider"

// Credential is the shape stored in `goods_providers.credentials_ciphertext`.
//
// It is JSON rather than a bare token because a gateway needs both an address
// and a secret, and an operator who moves their gateway should not have to
// touch two fields that can disagree.
type Credential struct {
	BaseURL string `json:"baseUrl"`
	Token   string `json:"token"`
	// Currency the gateway prices in, when it differs from the installation
	// default.
	Currency string `json:"currency,omitempty"`
}

// Registry resolves a provider slug to a configured adapter.
//
// It reads and decrypts on every call rather than caching. A credential an
// operator rotates in the panel then takes effect on the next delivery instead
// of at the next restart, and the cost is one indexed row read on a path that
// is about to make a network request anyway.
type Registry struct {
	pool     *pgxpool.Pool
	secrets  cipher.AEAD
	currency string

	direct     *DirectOptions
	directOnce sync.Once
	directErr  error
	directImpl *fragmentdirect.Provider
}

// DirectOptions configures the fragment_direct adapter from the environment.
type DirectOptions struct {
	Config fragmentdirect.Config
	Rates  *tonrate.Service
	Logger *slog.Logger
}

// WithFragmentDirect enables the direct Fragment adapter. Its secrets are read
// from the environment by the caller; the registry holds them only in memory.
func (registry *Registry) WithFragmentDirect(options DirectOptions) *Registry {
	registry.direct = &options
	return registry
}

// FragmentDirect builds the direct adapter once and reuses it.
//
// It is built once rather than per call, unlike the gateway, because its
// configuration cannot change without a restart — it lives in the environment
// — and deriving the wallet from the mnemonic is deliberately slow key
// stretching. It is returned whether or not an operator has enabled the
// provider, so the panel can show its wallet while it is still switched off.
func (registry *Registry) FragmentDirect() (*fragmentdirect.Provider, error) {
	if registry == nil || registry.direct == nil {
		return nil, ErrProviderUnconfigured
	}
	registry.directOnce.Do(func() {
		registry.directImpl, registry.directErr = fragmentdirect.New(
			registry.direct.Config, registry.direct.Rates, registry.direct.Logger)
	})
	if registry.directErr != nil {
		return nil, fmt.Errorf("%w: %w", ErrProviderUnconfigured, registry.directErr)
	}
	return registry.directImpl, nil
}

// DirectStatus is what the panel can say about the direct adapter without
// revealing any of its secrets.
type DirectStatus struct {
	Available  bool
	Configured bool
	DryRun     bool
	Problem    string
}

// FragmentDirectStatus reports whether the direct adapter is usable.
func (registry *Registry) FragmentDirectStatus() DirectStatus {
	if registry == nil || registry.direct == nil {
		return DirectStatus{}
	}
	status := DirectStatus{
		Available: true, Configured: registry.direct.Config.Configured(), DryRun: registry.direct.Config.DryRun,
	}
	if _, err := registry.FragmentDirect(); err != nil {
		// The error names which setting is wrong, never its value.
		status.Problem = registry.directErr.Error()
		status.Configured = false
	}
	return status
}

// NewRegistry builds the resolver.
//
// The encryption key is the same APP_DATA_ENCRYPTION_KEY the API seals with;
// the worker holds it because it is the process that calls the provider.
func NewRegistry(pool *pgxpool.Pool, encryptionKey []byte, defaultCurrency string) (*Registry, error) {
	if pool == nil {
		return nil, errors.New("a database pool is required")
	}
	if len(encryptionKey) != 32 {
		return nil, errors.New("digital goods credential key must be 32 bytes")
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	currency := strings.ToUpper(strings.TrimSpace(defaultCurrency))
	if currency == "" {
		currency = "RUB"
	}
	return &Registry{pool: pool, secrets: aead, currency: currency}, nil
}

// Provider returns the adapter for a slug, or the reason it cannot be used.
func (registry *Registry) Provider(ctx context.Context, slug string) (goods.Provider, error) {
	row, err := dbgen.New(registry.pool).GetGoodsProvider(ctx, slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProviderUnconfigured
	}
	if err != nil {
		return nil, err
	}
	if !row.Enabled {
		return nil, ErrProviderDisabled
	}
	if slug == fragmentdirect.Slug {
		// Its secrets come from the environment, never from this row.
		direct, directErr := registry.FragmentDirect()
		if directErr != nil {
			return nil, directErr
		}
		return direct, nil
	}
	if len(row.CredentialsCiphertext) == 0 {
		return nil, ErrProviderUnconfigured
	}

	plaintext, err := registry.open(row.CredentialsCiphertext)
	if err != nil {
		return nil, err
	}
	var credential Credential
	if err := json.Unmarshal([]byte(plaintext), &credential); err != nil {
		return nil, ErrProviderUnconfigured
	}
	currency := credential.Currency
	if currency == "" {
		currency = registry.currency
	}

	switch slug {
	case "fragment":
		return goods.NewFragment(goods.FragmentOptions{
			BaseURL: credential.BaseURL, Token: credential.Token, Currency: currency,
		})
	default:
		// A row naming a provider this build does not implement is a
		// configuration error, not a delivery failure. Refusing here means the
		// delivery is deferred rather than refunded.
		return nil, ErrProviderUnknown
	}
}

func (registry *Registry) open(ciphertext []byte) (string, error) {
	size := registry.secrets.NonceSize()
	if len(ciphertext) < size {
		return "", errors.New("stored credential is truncated")
	}
	plaintext, err := registry.secrets.Open(
		nil, ciphertext[:size], ciphertext[size:], []byte(credentialLabel),
	)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}
