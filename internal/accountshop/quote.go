package accountshop

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/accountpg"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/goods"
	"github.com/omniflow/omniflow/internal/payments"
)

// Quote is a price and the moment it stops being honourable.
//
// The cost the provider asked is carried unexported. It is the operator's
// margin, it never leaves this package, and keeping it off the exported surface
// means no transport layer can publish it by accident — while the order still
// records it, so the finance view can report a realised margin later.
type Quote struct {
	PriceMinor int64
	Currency   string
	// ExpiresAt is when this number stops being the number that will be
	// charged. It is always set: a quote without one promises nothing, and a
	// panel cannot warn a customer about a window it was never told about.
	ExpiresAt time.Time

	costMinor int64
	costKnown bool
}

// PromoState is what a code does to a quote, priced without redeeming it.
//
// A rejection keeps its own reason rather than collapsing into "invalid",
// because the customer's next move differs: an unknown code is a typo, an
// exhausted one is too late, and one refused for taking the order below the
// provider's cost is the right code on the wrong product.
type PromoState struct {
	Code          string
	DiscountMinor int64
	Rejection     string
}

// Detail is a product together with a fresh quote for it.
type Detail struct {
	Product  Product
	Quote    Quote
	Quantity int
	Promo    PromoState
}

// maxQuantity bounds one purchase.
//
// The ceiling is deliberately low. A shop order is delivered in one submission
// to a gateway that cannot be asked to undo it, so a mistyped quantity is
// unrecoverable in exactly the way a mistyped recipient is. An operator selling
// larger amounts configures a product for it, which is a decision recorded in
// the catalogue rather than typed into a form.
const maxQuantity = 10

// Detail quotes one product the moment the customer opens it.
//
// Quoting here rather than at confirmation is the whole point: the number on
// the screen is the number that will be charged, and its expiry travels with it
// so the panel can re-quote before the window closes instead of discovering at
// the till that the price moved.
func (service *Service) Detail(
	ctx context.Context, customerID, productID, locale string, quantity int, promoCode string,
) (Detail, error) {
	quantity = normalizeQuantity(quantity)
	if quantity == 0 {
		return Detail{}, accountpg.ErrInvalidInput
	}
	product, err := service.product(ctx, productID, locale)
	if err != nil {
		return Detail{}, err
	}
	quote, err := service.quote(ctx, product, quantity)
	if err != nil {
		return Detail{}, err
	}
	return Detail{
		Product: product, Quote: quote, Quantity: quantity,
		Promo: service.previewPromo(ctx, customerID, product, quote, promoCode),
	}, nil
}

// quote prices one purchase.
//
// A product with a fixed price never touches the provider: the operator has
// already decided what the customer pays and absorbs the variance, so asking a
// gateway would only add a way for the screen to fail. Everything else derives
// the price from what the provider charges, which is precisely why those quotes
// expire.
//
// The arithmetic is not repeated here. Markup, rounding, and the currency's
// exponent all come from internal/goods, so the web shop charges what the bot
// charges because both ask the same function rather than because two
// implementations happen to agree.
func (service *Service) quote(ctx context.Context, product Product, quantity int) (Quote, error) {
	expiry := service.now().Add(product.quoteTTL)
	if product.fixedMinor != nil {
		return Quote{
			PriceMinor: *product.fixedMinor * int64(quantity),
			Currency:   product.Currency, ExpiresAt: expiry,
		}, nil
	}

	provider, err := service.provider(ctx, product.ProviderSlug)
	if err != nil {
		return Quote{}, err
	}
	quoted, err := provider.Quote(ctx, goods.Request{
		Kind: product.Kind, DurationMonths: product.DurationMonths,
		StarQuantity: product.StarQuantity, Quantity: quantity,
		Currency: product.Currency,
	})
	switch {
	case errors.Is(err, goods.ErrCostUnavailable):
		// The gateway sells at a price it will not decompose, and no fixed price
		// is configured. There is nothing to charge.
		return Quote{}, ErrPriceUnavailable
	case err != nil:
		service.logger.Warn("digital goods quote failed",
			"product", product.Code, "provider", product.ProviderSlug, "error", err)
		return Quote{}, ErrPriceUnavailable
	}
	if !quoted.ExpiresAt.IsZero() {
		// A provider that states its own window owns it; the configured TTL is
		// the fallback for one whose rate it will not talk about.
		expiry = quoted.ExpiresAt.UTC()
	}

	exponent := currencyExponent(product.Currency)
	return Quote{
		PriceMinor: goods.Price(quoted.CostMinor, product.pricingRule(), exponent) * int64(quantity),
		Currency:   product.Currency,
		ExpiresAt:  expiry,
		costMinor:  quoted.CostMinor * int64(quantity),
		costKnown:  true,
	}, nil
}

// previewPromo prices a code against this quote without redeeming it.
//
// It re-validates rather than trusting whatever the customer last saw: a code
// that was good when it was typed may not be now, because the promotion can
// have ended and the provider rate can have moved and left no headroom under
// the cost floor. The preview opens the same transaction the purchase would and
// rolls it back, so looking at a discount can never spend it.
func (service *Service) previewPromo(
	ctx context.Context, customerID string, product Product, quote Quote, code string,
) PromoState {
	code = strings.TrimSpace(code)
	if code == "" {
		return PromoState{}
	}
	discount, err := service.orders.PreviewGoodsDiscount(ctx, commercepg.GoodsOrderInput{
		CustomerID: customerID, ProductID: product.ID,
		PriceMinor: quote.PriceMinor, CostMinor: quote.costMinor,
		CostKnown: quote.costKnown, Currency: quote.Currency,
		PromoCode: code, OperatorPriced: product.fixedMinor != nil,
	})
	if err != nil {
		return PromoState{Code: code, Rejection: promoRejection(err)}
	}
	return PromoState{Code: code, DiscountMinor: discount}
}

// promoRejection maps a refusal onto the machine reason a screen renders. The
// vocabulary matches the bot's, so the two surfaces explain a refused code the
// same way.
func promoRejection(err error) string {
	switch {
	case errors.Is(err, commercepg.ErrPromoUnknown):
		return "unknown"
	case errors.Is(err, commercepg.ErrPromoExhausted):
		return "exhausted"
	case errors.Is(err, commercepg.ErrPromoBelowCost):
		return "below_cost"
	case errors.Is(err, commercepg.ErrPromoIneligible):
		return "ineligible"
	default:
		return "invalid"
	}
}

// evaluateQuote decides whether the price a customer is looking at may still be
// charged.
//
// It is the whole of the "shown is charged" guarantee, and it is pure so it can
// be reasoned about and tested without a gateway. The quote the client submits
// is treated as an assertion to check, never as an amount to trust: the charge
// always comes from `fresh`, so a client that invents a lower price is refused
// rather than believed, and one that invents a later expiry still has to match
// the number the provider just gave.
//
// The two refusals stay separate because the panel's response differs. An
// expired quote is re-quoted silently — nothing was promised past its window. A
// changed price has to be shown again, because the customer agreed to a
// different number and is entitled to see the new one before agreeing to it.
func evaluateQuote(shown, fresh Quote, now time.Time) error {
	// No expiry means no quote was ever issued to this client, which is the same
	// condition as a stale one: nothing was promised, and the remedy is the same.
	if shown.ExpiresAt.IsZero() || !shown.ExpiresAt.After(now) {
		return ErrQuoteExpired
	}
	if !strings.EqualFold(shown.Currency, fresh.Currency) || shown.PriceMinor != fresh.PriceMinor {
		return ErrPriceChanged
	}
	return nil
}

// normalizeQuantity clamps what the customer asked for, answering zero for
// anything the shop refuses to sell in one go.
func normalizeQuantity(quantity int) int {
	if quantity == 0 {
		// Omitted rather than chosen. One is what every catalogue entry means on
		// its own, and it is what the bot buys.
		return 1
	}
	if quantity < 0 || quantity > maxQuantity {
		return 0
	}
	return quantity
}

// currencyExponent reports how many minor units make up a major one, falling
// back to zero for a currency the table does not know. Zero is the safe answer:
// the unit-rounding modes then round to a whole minor unit, which an integer
// amount already is, so an unknown currency loses the rounding step rather than
// scaling a price by a hundred.
func currencyExponent(currency string) int {
	exponent, err := payments.CurrencyExponent(strings.ToUpper(strings.TrimSpace(currency)))
	if err != nil {
		return 0
	}
	return exponent
}
