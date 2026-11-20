//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/payments"
	"github.com/omniflow/omniflow/internal/paymentservice"
)

// An operator's credit recorded as a payment is a paid top-up through the
// manual adapter: the wallet holds it once however often the click is
// retried, it is refused as a debit, and the panel refuses it when no payment
// service is wired in.
func TestOperatorCreditAsPaymentSettlesOnce(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	service := paymentservice.New(harness.pool, store, payments.Manual{})
	panel, err := panelpg.New(harness.pool, operationsKey, panelpg.Options{})
	if err != nil {
		t.Fatalf("panel: %v", err)
	}
	customerID := harness.customer(ctx, t)
	adjustment := panelpg.WalletAdjustment{AmountMinor: 25000, Currency: "RUB", Reason: "Bank transfer received", IdempotencyKey: "credit-as-payment-1", AsPayment: true}
	actor := panelpg.Actor{AdminID: "00000000-0000-4000-8000-000000000001", Reason: "Bank transfer received"}

	if err = panel.AdjustCustomerWallet(ctx, customerID, adjustment, actor); !errors.Is(err, panelpg.ErrPaymentCreditUnavailable) {
		t.Fatalf("without a payment service: %v", err)
	}
	panel.WithPaymentCredit(service)
	for range 2 {
		if err = panel.AdjustCustomerWallet(ctx, customerID, adjustment, actor); err != nil {
			t.Fatalf("credit: %v", err)
		}
	}
	var balance int64
	var paidTopups int
	if err = harness.pool.QueryRow(ctx, `SELECT COALESCE(sum(amount_minor), 0)::bigint FROM ledger_entries
		WHERE account_type = 'customer_wallet' AND user_id = $1::uuid`, customerID).Scan(&balance); err != nil {
		t.Fatalf("balance: %v", err)
	}
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM orders
		WHERE user_id = $1::uuid AND operation = 'topup' AND state = 'paid'`, customerID).Scan(&paidTopups); err != nil {
		t.Fatalf("orders: %v", err)
	}
	if balance != 25000 || paidTopups != 1 {
		t.Fatalf("balance=%d paid top-ups=%d, want one credit of 25000", balance, paidTopups)
	}
	adjustment.AmountMinor, adjustment.IdempotencyKey = -100, "credit-as-payment-2"
	if err = panel.AdjustCustomerWallet(ctx, customerID, adjustment, actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("a debit as a payment: %v", err)
	}
}
