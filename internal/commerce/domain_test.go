package commerce

import (
	"errors"
	"testing"
	"time"
)

func TestWalletFirstOrderApplication(t *testing.T) {
	price, _ := NewMoney(1000, "RUB")
	order, err := NewOrder("order", "customer", price, 100, 250)
	if err != nil {
		t.Fatal(err)
	}
	if order.WalletMinor != 250 || order.ExternalMinor != 650 {
		t.Fatalf("unexpected split: wallet=%d external=%d", order.WalletMinor, order.ExternalMinor)
	}
}

func TestPaymentMismatchAndDuplicateAreSafe(t *testing.T) {
	price, _ := NewMoney(1000, "RUB")
	order, _ := NewOrder("order", "customer", price, 0, 0)
	order.State = OrderPending
	wrongCurrency, _ := NewMoney(1000, "USD")
	if _, reason, err := order.ApplyPayment(PaymentResult{Amount: wrongCurrency}); !errors.Is(err, ErrCurrencyMismatch) || reason != "currency_mismatch" {
		t.Fatalf("expected currency mismatch, got %q %v", reason, err)
	}
	paid, reason, err := order.ApplyPayment(PaymentResult{Amount: price})
	if err != nil || reason != "paid" || paid.State != OrderPaid {
		t.Fatalf("payment failed: %#v %s %v", paid, reason, err)
	}
	duplicate, reason, err := paid.ApplyPayment(PaymentResult{Amount: price})
	if err != nil || reason != "duplicate" || duplicate.PaidMinor != paid.PaidMinor {
		t.Fatalf("duplicate changed state: %#v %s %v", duplicate, reason, err)
	}
}

func TestPaymentClassifiesUnderOverAndLateWithoutUnsafeSettlement(t *testing.T) {
	price, _ := NewMoney(1000, "RUB")
	order, _ := NewOrder("order", "customer", price, 0, 0)
	order.State = OrderPending

	under, _ := NewMoney(999, "RUB")
	unchanged, reason, err := order.ApplyPayment(PaymentResult{Amount: under})
	if err != nil || reason != "underpayment" || unchanged.State != OrderPending || unchanged.PaidMinor != 0 {
		t.Fatalf("underpayment changed the order: %#v %q %v", unchanged, reason, err)
	}

	over, _ := NewMoney(1001, "RUB")
	unchanged, reason, err = order.ApplyPayment(PaymentResult{Amount: over})
	if err != nil || reason != "overpayment" || unchanged.State != OrderPending || unchanged.PaidMinor != 1001 {
		t.Fatalf("overpayment classification was lost: %#v %q %v", unchanged, reason, err)
	}

	paid, reason, err := order.ApplyPayment(PaymentResult{Amount: price, Late: true})
	if err != nil || reason != "late" || paid.State != OrderPaid {
		t.Fatalf("late exact payment should remain fulfillable: %#v %q %v", paid, reason, err)
	}
}

func TestLedgerMustBalancePerCurrency(t *testing.T) {
	valid := []LedgerEntry{
		{AccountType: "customer_wallet", CustomerID: "customer", Currency: "RUB", AmountMinor: 500},
		{AccountType: "platform_clearing", Currency: "RUB", AmountMinor: -500},
	}
	if err := ValidateLedger(valid); err != nil {
		t.Fatal(err)
	}
	invalid := append(valid, LedgerEntry{AccountType: "customer_wallet", CustomerID: "customer", Currency: "USD", AmountMinor: 1})
	if !errors.Is(ValidateLedger(invalid), ErrUnbalancedLedger) {
		t.Fatal("expected per-currency balance failure")
	}
}

func TestWalletBalanceRejectsOverspendAndIsCurrencyIsolated(t *testing.T) {
	entries := []LedgerEntry{
		{AccountType: "customer_wallet", CustomerID: "customer", Currency: "RUB", AmountMinor: 500},
		{AccountType: "customer_wallet", CustomerID: "customer", Currency: "RUB", AmountMinor: -200},
		{AccountType: "customer_wallet", CustomerID: "customer", Currency: "USD", AmountMinor: 900},
	}
	balance, err := WalletBalance(entries, "customer", "RUB")
	if err != nil || balance != 300 {
		t.Fatalf("unexpected RUB balance: %d %v", balance, err)
	}
	entries = append(entries, LedgerEntry{AccountType: "customer_wallet", CustomerID: "customer", Currency: "RUB", AmountMinor: -400})
	if _, err := WalletBalance(entries, "customer", "RUB"); err == nil {
		t.Fatal("expected negative wallet invariant failure")
	}
}

func TestPromotionBoundsAndScope(t *testing.T) {
	price, _ := NewMoney(1000, "RUB")
	now := time.Now()
	promotion := Promotion{Kind: "percent", Value: 2500, PlanIDs: map[string]struct{}{"basic": {}}, CustomerLimit: 1}
	discount, err := promotion.Discount(now, "customer", "basic", price)
	if err != nil || discount.Amount != 250 {
		t.Fatalf("unexpected discount: %#v %v", discount, err)
	}
	if _, err := promotion.Discount(now, "customer", "other", price); !errors.Is(err, ErrPromotionInvalid) {
		t.Fatalf("expected scope rejection, got %v", err)
	}
}

func TestEntitlementSchedulesDoNotImplicitlyProrate(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	currentEnd := now.Add(24 * time.Hour)
	duration := 30 * 24 * time.Hour

	extension, err := ScheduleEntitlement(now, duration, "extension", "extend", "at_expiry", &currentEnd)
	if err != nil || !extension.EffectiveAt.Equal(now) || !extension.EndsAt.Equal(currentEnd.Add(duration)) {
		t.Fatalf("unexpected extension: %#v %v", extension, err)
	}
	downgrade, err := ScheduleEntitlement(now, duration, "downgrade", "replace", "at_expiry", &currentEnd)
	if err != nil || !downgrade.EffectiveAt.Equal(currentEnd) || !downgrade.StartsAt.Equal(currentEnd) {
		t.Fatalf("unexpected deferred downgrade: %#v %v", downgrade, err)
	}
	if _, err := ScheduleEntitlement(now, duration, "upgrade", "forbid", "at_expiry", &currentEnd); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected forbidden upgrade, got %v", err)
	}
}
