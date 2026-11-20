package botapp

import (
	"context"
	"sync"

	"github.com/omniflow/omniflow/internal/fxrate"
)

var (
	ratesOnce    sync.Once
	ratesService *fxrate.Service
)

// rates is the shared exchange-rate reader. It is built on first use from
// the store's pool, so the bot needs no extra wiring for it.
func (service *Commerce) rates() *fxrate.Service {
	ratesOnce.Do(func() { ratesService = fxrate.New(service.store.pool, fxrate.Options{}) })
	return ratesService
}

// StarsQuote is how many Stars an order costs. An order priced in Stars costs
// its own amount; one priced in the currency the operator converts costs the
// fixed quote, the same number the invoice, Telegram's pre-checkout probe, and
// settlement all read.
func (service *Commerce) StarsQuote(ctx context.Context, order OrderSummary) (int64, error) {
	if order.Currency == "XTR" {
		return order.ExternalMinor, nil
	}
	quote, err := service.rates().QuoteOrder(ctx, order.ID)
	if err != nil {
		return 0, err
	}
	return quote.Stars, nil
}

// StarsPayable reports whether Stars can pay an order in its currency.
func (service *Commerce) StarsPayable(currency string) bool {
	return currency == "XTR" || service.rates().Convertible(currency)
}
