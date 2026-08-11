package botapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/goods"
)

// GoodsProviders resolves a product's provider slug to an adapter.
//
// It is an interface so the credential is unsealed at call time: a gateway
// token an operator rotates in the panel takes effect on the next quote rather
// than at the next restart. A nil registry disables the shop, which is what an
// installation with no digital-goods provider gets.
type GoodsProviders interface {
	Provider(ctx context.Context, slug string) (goods.Provider, error)
}

// EnableShop attaches the digital-goods adapters.
func (service *Commerce) EnableShop(providers GoodsProviders) {
	service.goods = providers
}

// ShopEnabled reports whether the shop can quote a price.
func (service *Commerce) ShopEnabled() bool { return service != nil && service.goods != nil }

// ShopQuote is what a customer would pay for one shop purchase.
//
// Both halves of the quote are carried. `CostMinor` is what the provider asked
// and never reaches the customer; `PriceMinor` is what they pay. Keeping them
// apart is what lets the finance view report a realised margin later without
// recomputing a markup that may since have changed.
type ShopQuote struct {
	PriceMinor int64
	CostMinor  int64
	CostKnown  bool
	Currency   string
	ExpiresAt  time.Time
}

// ErrShopUnavailable reports that no digital-goods provider is configured.
var ErrShopUnavailable = errors.New("the digital goods shop is not configured")

// QuoteGoods prices one purchase.
//
// A product with a fixed price never touches the provider: the operator has
// already decided what the customer pays and absorbs the variance, so asking a
// gateway would only add a way for the screen to fail. Everything else derives
// the price from what the provider charges, which is why those quotes expire.
//
// A provider that cannot state its own cost is not an error. Telegram Premium
// is sold at a published price the gateway does not break down, so the quote is
// honest about the cost being unknown and the order records that rather than a
// fabricated margin.
func (service *Commerce) QuoteGoods(
	ctx context.Context, product ShopProduct, quantity int,
) (ShopQuote, error) {
	rule := product.PricingRule()
	expiry := service.clock().UTC().Add(product.QuoteTTL)
	if product.FixedMinor != nil {
		return ShopQuote{
			PriceMinor: *product.FixedMinor * int64(quantity),
			Currency:   product.Currency, ExpiresAt: expiry,
		}, nil
	}
	if !service.ShopEnabled() {
		return ShopQuote{}, ErrShopUnavailable
	}
	provider, err := service.goods.Provider(ctx, product.ProviderSlug)
	if err != nil {
		return ShopQuote{}, err
	}
	quoted, err := provider.Quote(ctx, goods.Request{
		Kind: product.Kind, DurationMonths: product.DurationMonths,
		StarQuantity: product.StarQuantity, Quantity: quantity,
		Currency: product.Currency,
	})
	if errors.Is(err, goods.ErrCostUnavailable) {
		// The provider sells at a price it will not decompose. Without a fixed
		// price configured there is nothing to charge, and inventing one would
		// be charging a number nobody chose.
		return ShopQuote{}, goods.ErrCostUnavailable
	}
	if err != nil {
		return ShopQuote{}, err
	}
	if !quoted.ExpiresAt.IsZero() {
		expiry = quoted.ExpiresAt
	}
	exponent := currencyExponent(product.Currency)
	return ShopQuote{
		PriceMinor: goods.Price(quoted.CostMinor, rule, exponent) * int64(quantity),
		CostMinor:  quoted.CostMinor * int64(quantity),
		CostKnown:  true,
		Currency:   product.Currency,
		ExpiresAt:  expiry,
	}, nil
}

// StartShopPurchase opens the order for one shop purchase.
//
// The idempotency key names the customer, the product, the recipient, and the
// quote window. A customer who taps confirm twice reaches the same order rather
// than buying twice; one who comes back after the quote expired gets a new
// order at a price that is still honourable.
func (service *Commerce) StartShopPurchase(
	ctx context.Context, customerID string, product ShopProduct, quantity int,
	recipient string, isSelf bool, quote ShopQuote, skipWallet bool,
) (OrderSummary, error) {
	normalized, err := goods.NormalizeRecipient(recipient)
	if err != nil {
		return OrderSummary{}, err
	}
	order, err := service.orders.CreateGoodsOrder(ctx, commercepg.GoodsOrderInput{
		CustomerID: customerID, ProductID: product.ID, Quantity: quantity,
		Recipient: normalized, RecipientIsSelf: isSelf,
		CostMinor: quote.CostMinor, PriceMinor: quote.PriceMinor, CostKnown: quote.CostKnown,
		Currency: quote.Currency, QuoteExpiresAt: quote.ExpiresAt,
		SkipWallet: skipWallet,
		IdempotencyKey: fmt.Sprintf("goods:%s:%s:%s:%d",
			customerID, product.ID, normalized, quote.ExpiresAt.Unix()),
	})
	if err != nil {
		return OrderSummary{}, err
	}
	return service.store.Order(ctx, customerID, uuidText(order.ID), LocaleEnglish)
}
