//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/panelpg"
)

// From the customer card an operator credits or debits the wallet — once per
// click, never below zero, optionally telling the customer — and writes the
// customer a message the bot delivers.
func TestOperatorWalletAdjustmentAndMessage(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service, err := panelpg.New(harness.pool, operationsKey, panelpg.Options{})
	if err != nil {
		t.Fatal(err)
	}
	customer := harness.customer(ctx, t)
	actor := panelpg.Actor{Type: "admin", Reason: "compensation for the outage"}

	credit := panelpg.WalletAdjustment{AmountMinor: 15000, Currency: "rub", Reason: "Компенсация", Notify: true, IdempotencyKey: "panel-credit-0001"}
	for range 2 {
		if err := service.AdjustCustomerWallet(ctx, customer, credit, actor); err != nil {
			t.Fatalf("credit: %v", err)
		}
	}
	if balance := harness.walletBalance(ctx, t, customer); balance != 15000 {
		t.Fatalf("balance after a repeated click = %d, want 15000", balance)
	}
	debit := panelpg.WalletAdjustment{AmountMinor: -20000, Currency: "RUB", Reason: "Ошибка", IdempotencyKey: "panel-debit-0001"}
	if err := service.AdjustCustomerWallet(ctx, customer, debit, actor); !errors.Is(err, panelpg.ErrNegativeBalance) {
		t.Fatalf("overdraw: %v, want ErrNegativeBalance", err)
	}
	if _, err := service.MessageCustomer(ctx, customer, "Здравствуйте! Сервер в Нидерландах снова работает.", actor); err != nil {
		t.Fatalf("message: %v", err)
	}
	if _, err := service.MessageCustomer(ctx, customer, "   ", actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("empty message: %v", err)
	}
	var pending int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM customer_direct_messages WHERE user_id = $1::uuid AND status = 'pending'`, customer).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 2 {
		t.Fatalf("pending messages = %d, want the balance notice and the operator's message", pending)
	}
}
