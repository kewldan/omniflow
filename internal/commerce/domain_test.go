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

// A payment that lands on a closed order is money the customer sent. It
// settles, so they get what they paid for, but it is named so that nobody
// reads it as an ordinary sale: a cancelled order was one the customer was
// told would not be charged.
func TestPaymentOnAClosedOrderSettlesAsLateAndIsNamed(t *testing.T) {
	price, _ := NewMoney(1000, "RUB")
	order, _ := NewOrder("order", "customer", price, 0, 0)

	order.State = OrderCancelled
	paid, reason, err := order.ApplyPayment(PaymentResult{Amount: price})
	if err != nil || reason != ClassificationPaidAfterCancellation || paid.State != OrderPaid || paid.PaidMinor != 1000 {
		t.Fatalf("payment on a cancelled order: %#v %q %v", paid, reason, err)
	}
	if !NeedsOperator(reason) {
		t.Fatal("a payment after cancellation must reach an operator")
	}

	// An expired order is late whatever the caller knew about the window.
	order.State = OrderExpired
	paid, reason, err = order.ApplyPayment(PaymentResult{Amount: price, Late: false})
	if err != nil || reason != ClassificationLate || paid.State != OrderPaid {
		t.Fatalf("payment on an expired order: %#v %q %v", paid, reason, err)
	}
	if NeedsOperator(reason) {
		t.Fatal("an ordinary late payment settles without an operator")
	}

	// The amount rules still apply first: a short payment on a cancelled
	// order is an underpayment, not a settlement.
	order.State = OrderCancelled
	under, _ := NewMoney(999, "RUB")
	unchanged, reason, err := order.ApplyPayment(PaymentResult{Amount: under})
	if err != nil || reason != ClassificationUnderpayment || unchanged.State != OrderCancelled {
		t.Fatalf("short payment on a cancelled order: %#v %q %v", unchanged, reason, err)
	}

	// The documented transition map agrees with the settlement rule.
	for _, from := range []OrderState{OrderCancelled, OrderExpired} {
		if _, err := (Order{State: from}).Transition(OrderPaid); err != nil {
			t.Fatalf("%s -> paid must be a documented transition: %v", from, err)
		}
	}
}

// Buying a new period restarts the traffic counter; provisioning a new user
// has nothing to restart, and a downgrade keeps the current period's count.
func TestOnlyANewPeriodResetsTraffic(t *testing.T) {
	for operation, want := range map[string]bool{
		"extension": true, "renewal": true, "upgrade": true,
		"purchase": false, "downgrade": false, "addon": false, "topup": false, "gift": false, "code": false,
	} {
		if got := ResetsTraffic(operation); got != want {
			t.Errorf("ResetsTraffic(%q) = %v, want %v", operation, got, want)
		}
	}
}

func TestOperatorAttentionClassifications(t *testing.T) {
	for classification, want := range map[string]bool{
		ClassificationPaid: false, ClassificationLate: false,
		ClassificationDuplicate: true, ClassificationUnderpayment: true, ClassificationOverpayment: true,
		ClassificationCurrency: true, ClassificationWallet: true, ClassificationPaidAfterCancellation: true,
	} {
		if got := NeedsOperator(classification); got != want {
			t.Errorf("NeedsOperator(%q) = %v, want %v", classification, got, want)
		}
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
