package botapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/gifts"
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
	recipient string, isSelf bool, quote ShopQuote, skipWallet bool, promoCode string,
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
		SkipWallet: skipWallet, PromoCode: promoCode,
		// A fixed price is one the operator chose, so it carries no provider
		// cost floor. Deriving this here keeps the rule in one place rather than
		// asking every caller to remember it.
		OperatorPriced: product.FixedMinor != nil,
		IdempotencyKey: fmt.Sprintf("goods:%s:%s:%s:%d",
			customerID, product.ID, normalized, quote.ExpiresAt.Unix()),
	})
	if err != nil {
		return OrderSummary{}, err
	}
	return service.store.Order(ctx, customerID, uuidText(order.ID), LocaleEnglish)
}

// GiftPurchase is one gift as the sender sees it immediately after buying.
//
// `Code` is populated only on the response that created the gift. A later read
// leaves it empty, because only the hash was kept.
type GiftPurchase struct {
	OrderID   string
	GiftID    string
	Code      string
	CodeHint  string
	Kind      string
	Currency  string
	ExpiresAt time.Time
}

// BuyGift opens a gift order for the sender.
func (service *Commerce) BuyGift(
	ctx context.Context, input commercepg.GiftOrderInput,
) (GiftPurchase, error) {
	purchase, err := service.orders.CreateGiftOrder(ctx, input)
	if err != nil {
		return GiftPurchase{}, err
	}
	return GiftPurchase{
		OrderID: uuidText(purchase.Order.ID), GiftID: uuidText(purchase.Gift.ID),
		Code: purchase.Code, CodeHint: purchase.Gift.CodeHint,
		Kind: purchase.Gift.Kind, Currency: purchase.Gift.Currency,
		ExpiresAt: purchase.Gift.ExpiresAt.Time,
	}, nil
}

// ClaimedGift is what a recipient was given.
type ClaimedGift struct {
	Kind        string
	CreditMinor int64
	Currency    string
	// EndsAt is set when the code granted a subscription, so the confirmation
	// can say what the customer now has rather than only that it worked.
	EndsAt time.Time
}

// ClaimGift redeems a code for the claiming customer.
//
// It accepts either kind of code, and that is the point rather than a
// convenience. A gift code and a wholesale batch code are the same sixteen
// characters — see internal/accesscode — and a customer holding one has no way
// to know which they were handed. Asking them to pick the right form would be
// asking them to know something only the operator does.
//
// The gift table is tried first because a gift carries more state to get wrong:
// a named recipient, a sender who must not claim their own, an attempt counter.
// A code the gift table does not recognise falls through to the batch table,
// and a failure that is not "no such gift" is returned as it is rather than
// being retried against a table that could not possibly hold it.
func (service *Commerce) ClaimGift(
	ctx context.Context, code, claimantID string,
) (ClaimedGift, error) {
	gift, err := service.orders.ClaimGift(ctx, code, claimantID)
	if err == nil {
		return ClaimedGift{
			Kind: gift.Kind, CreditMinor: gift.CreditMinor.Int64, Currency: gift.Currency,
		}, nil
	}
	if !errors.Is(err, commercepg.ErrGiftNotFound) {
		return ClaimedGift{}, err
	}

	redeemed, redeemErr := service.orders.RedeemAccessCode(ctx, code, claimantID)
	if redeemErr != nil {
		// The original refusal is what the caller renders, and both refusals
		// render the same message anyway: a recipient learning the difference
		// between "no such code" and "already used" would be an attacker
		// learning which codes exist.
		return ClaimedGift{}, err
	}
	return ClaimedGift{Kind: gifts.KindSubscription, EndsAt: redeemed.EndsAt}, nil
}

// SavedShopPurchase is a shop selection the customer kept for later.
type SavedShopPurchase struct {
	CartID     string
	ProductID  string
	Quantity   int
	Recipient  string
	IsSelf     bool
	PromoCode  string
	SavedMinor int64
	Currency   string
}

// SaveShopPurchase keeps a selection without charging for it.
func (service *Commerce) SaveShopPurchase(
	ctx context.Context, customerID string, product ShopProduct,
	recipient string, isSelf bool, quote ShopQuote,
) error {
	_, err := service.orders.SaveGoodsCart(ctx, commercepg.GoodsCartInput{
		CustomerID: customerID, ProductID: product.ID, Quantity: 1,
		Recipient: recipient, IsSelf: isSelf,
		SavedPriceMinor: quote.PriceMinor, Currency: quote.Currency,
	})
	return err
}

// SavedShopPurchase reads the saved selection, if the customer's open cart is a
// shop one.
func (service *Commerce) SavedShopPurchase(
	ctx context.Context, customerID string,
) (SavedShopPurchase, bool, error) {
	cart, found, err := service.orders.OpenGoodsCart(ctx, customerID)
	if err != nil || !found {
		return SavedShopPurchase{}, false, err
	}
	return SavedShopPurchase{
		CartID:     uuidText(cart.ID),
		ProductID:  uuidText(cart.Line.ProductID),
		Quantity:   int(cart.Line.Quantity),
		Recipient:  cart.Line.RecipientUsername,
		IsSelf:     cart.Line.RecipientIsSelf,
		PromoCode:  cart.PromoCode.String,
		SavedMinor: cart.Line.SavedPriceMinor,
		Currency:   cart.Line.Currency,
	}, true, nil
}

// SetShopPromo attaches or clears the code on the saved selection.
func (service *Commerce) SetShopPromo(
	ctx context.Context, customerID, code string,
) (string, error) {
	cart, err := service.orders.SetGoodsCartPromo(ctx, customerID, code)
	if err != nil {
		return "", err
	}
	return cart.PromoCode.String, nil
}

// DiscardShopPurchase drops the saved selection.
func (service *Commerce) DiscardShopPurchase(ctx context.Context, customerID string) error {
	return service.orders.DiscardGoodsCart(ctx, customerID)
}

// PreviewShopPromo prices a code against a quote without redeeming it.
//
// It opens the same transaction the purchase would and rolls it back, so a
// preview can never consume a redemption — the same guarantee the plan preview
// gives.
func (service *Commerce) PreviewShopPromo(
	ctx context.Context, customerID string, product ShopProduct,
	quote ShopQuote, code string,
) (int64, string) {
	if code == "" {
		return 0, ""
	}
	discount, err := service.orders.PreviewGoodsDiscount(ctx, commercepg.GoodsOrderInput{
		CustomerID: customerID, ProductID: product.ID,
		PriceMinor: quote.PriceMinor, CostMinor: quote.CostMinor,
		CostKnown: quote.CostKnown, Currency: quote.Currency,
		PromoCode: code, OperatorPriced: product.FixedMinor != nil,
	})
	if err != nil {
		return 0, shopPromoRejection(err)
	}
	return discount, ""
}

// shopPromoRejection maps a refusal onto the machine reason a screen renders.
//
// The reasons stay distinct rather than collapsing into "invalid", because the
// customer's next move differs: an unknown code is a typo, an exhausted one is
// too late, and a below-cost one is the right code on the wrong product.
func shopPromoRejection(err error) string {
	switch {
	case errors.Is(err, commercepg.ErrPromoUnknown):
		return "unknown"
	case errors.Is(err, commercepg.ErrPromoExhausted):
		return "exhausted"
	case errors.Is(err, commercepg.ErrPromoBelowCost):
		return "belowCost"
	case errors.Is(err, commercepg.ErrPromoIneligible):
		return "ineligible"
	default:
		return "invalid"
	}
}

// CloseSavedShopPurchase marks the saved selection as bought.
func (service *Commerce) CloseSavedShopPurchase(ctx context.Context, cartID, orderID string) error {
	return service.orders.MarkGoodsCartPurchased(ctx, cartID, orderID)
}
