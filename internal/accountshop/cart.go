package accountshop

import (
	"context"
	"strings"

	"github.com/omniflow/omniflow/internal/accountpg"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// SaveRequest keeps a purchase the wallet cannot cover yet, to be bought
// automatically once a top-up lands.
type SaveRequest struct {
	CustomerID string
	ProductID  string
	Quantity   int
	Stars      int
	// Recipient is the reviewed handle, exactly as for a purchase.
	Recipient string
	ForSelf   bool
	// ShownPriceMinor and ShownCurrency are what the customer agreed to. The
	// saved cart remembers it, and the automatic purchase refuses to charge
	// more.
	ShownPriceMinor int64
	ShownCurrency   string
	PromoCode       string
	Locale          string
}

// TopUpPlan is what a customer needs to pay in to have the purchase made.
type TopUpPlan struct {
	PriceMinor     int64
	AvailableMinor int64
	// ShortfallMinor is exactly what is missing — the amount the panel offers
	// to top up, the fork's «К оплате N ₽».
	ShortfallMinor int64
	Currency       string
	Stars          int
}

// SaveForTopUp saves the purchase as the customer's open cart with automatic
// purchase on, and says how much is missing.
//
// It is offered only for a product the operator priced themselves. That price
// does not move with a provider rate, so buying it unattended once the top-up
// settles charges exactly what the customer was shown; the sweep re-checks it
// under the order's own lock and refuses a rise. A quote-priced product is
// refused here, because charging a quote unattended would charge a number the
// customer last saw before they went to pay.
func (service *Service) SaveForTopUp(ctx context.Context, request SaveRequest) (TopUpPlan, error) {
	if !service.Enabled() {
		return TopUpPlan{}, ErrUnavailable
	}
	quantity := normalizeQuantity(request.Quantity)
	if quantity == 0 {
		return TopUpPlan{}, accountpg.ErrInvalidInput
	}
	recipient, err := reviewedRecipient(request.Recipient)
	if err != nil {
		return TopUpPlan{}, err
	}
	product, err := service.product(ctx, request.ProductID, request.Locale)
	if err != nil {
		return TopUpPlan{}, err
	}
	if !product.Available {
		return TopUpPlan{}, ErrUnavailable
	}
	if !product.OperatorPriced() {
		return TopUpPlan{}, ErrNotOperatorPriced
	}
	quantity, stars, err := lineStars(product, quantity, request.Stars)
	if err != nil {
		return TopUpPlan{}, err
	}
	quote, err := service.quote(ctx, product, quantity, stars)
	if err != nil {
		return TopUpPlan{}, err
	}
	if quote.PriceMinor != request.ShownPriceMinor ||
		!strings.EqualFold(quote.Currency, strings.TrimSpace(request.ShownCurrency)) {
		return TopUpPlan{}, ErrPriceChanged
	}
	if _, err = service.orders.SaveGoodsCart(ctx, commercepg.GoodsCartInput{
		CustomerID: request.CustomerID, ProductID: product.ID, Quantity: quantity,
		Recipient: recipient, IsSelf: request.ForSelf,
		SavedPriceMinor: quote.PriceMinor, Currency: quote.Currency,
		StarQuantity: stars, AutoPurchase: true,
	}); err != nil {
		return TopUpPlan{}, err
	}
	if code := strings.TrimSpace(request.PromoCode); code != "" {
		// A code that does not parse is dropped rather than failing the save:
		// the purchase is what the customer is paying in for.
		_, _ = service.orders.SetGoodsCartPromo(ctx, request.CustomerID, code)
	}
	available, err := service.orders.AvailableWalletBalance(ctx, request.CustomerID, quote.Currency)
	if err != nil {
		return TopUpPlan{}, err
	}
	return TopUpPlan{
		PriceMinor: quote.PriceMinor, AvailableMinor: max(available, 0),
		ShortfallMinor: max(quote.PriceMinor-max(available, 0), 0),
		Currency:       quote.Currency, Stars: stars,
	}, nil
}

// BuySavedNow charges the saved cart from the wallet right away, for a
// customer whose balance already covers it. It shares the sweep's idempotency
// key, so it cannot race the automatic purchase into a second order.
func (service *Service) BuySavedNow(ctx context.Context, customerID string) (string, string, error) {
	if !service.Enabled() {
		return "", "", ErrUnavailable
	}
	purchase, err := service.orders.PurchaseCartNow(ctx, customerID)
	if err != nil {
		return "", "", err
	}
	return purchase.OrderID, purchase.Reason, nil
}
