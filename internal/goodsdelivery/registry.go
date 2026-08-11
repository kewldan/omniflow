package goodsdelivery

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/goods"
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
