package paymentservice

import (
	"context"
	"errors"
	"math/big"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/payments"
)

// recordQuote keeps what the provider was asked to collect when it is not the
// order's own amount and currency. It is written before the customer is given
// the checkout link, so no payment can arrive for a quote that was not kept.
func recordQuote(ctx context.Context, queries *dbgen.Queries, intent dbgen.PaymentIntent, created payments.Intent) error {
	charged := created.Charged
	if charged == nil || charged.Currency == intent.Currency || charged.Amount <= 0 {
		return nil
	}
	return queries.InsertPaymentQuote(ctx, dbgen.InsertPaymentQuoteParams{
		PaymentIntentID: intent.ID, AmountMinor: intent.AmountMinor, Currency: intent.Currency,
		ChargedMinor: charged.Amount, ChargedCurrency: charged.Currency,
	})
}

// inOrderCurrency maps money a provider reports in a quoted currency back onto
// the order's. The exact quoted amount settles the order's exact amount; any
// other amount is converted at the quote's own ratio, rounded down, and goes
// on to be classified like any short or long payment — so a customer who paid
// a cent less than asked has underpaid rather than been rounded into a sale.
//
// Money in the order's currency passes through unchanged, and so does money
// in a currency no quote names: settlement refuses that as a currency
// mismatch, which is the right answer for a payment nobody priced.
func inOrderCurrency(ctx context.Context, queries *dbgen.Queries, intent dbgen.PaymentIntent, received commerce.Money) (commerce.Money, error) {
	if received.Currency == "" || received.Currency == intent.Currency {
		return received, nil
	}
	quote, err := queries.GetPaymentQuote(ctx, intent.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return received, nil
	}
	if err != nil {
		return commerce.Money{}, err
	}
	if quote.ChargedCurrency != received.Currency {
		return received, nil
	}
	if received.Amount == quote.ChargedMinor {
		return commerce.Money{Amount: quote.AmountMinor, Currency: quote.Currency}, nil
	}
	converted := new(big.Int).Mul(big.NewInt(received.Amount), big.NewInt(quote.AmountMinor))
	converted.Quo(converted, big.NewInt(quote.ChargedMinor))
	return commerce.Money{Amount: converted.Int64(), Currency: quote.Currency}, nil
}
