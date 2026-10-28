package commerce

import (
	"testing"
	"time"
)

// The promotion rule is one rule whether it is applied when an order is opened
// or when a closed order is paid late: any set limit reached, or the
// per-customer limit reached, refuses.
func TestPromoLimitReachedChecksEveryLimitThatIsSet(t *testing.T) {
	one, two := 1, 2
	cases := []struct {
		name                   string
		promotion, code        *int
		perCustomer            int
		total, codeCount, mine int
		want                   bool
	}{
		{"nothing used", &two, &two, 1, 0, 0, 0, false},
		{"promotion total reached", &one, nil, 5, 1, 0, 0, true},
		{"code limit reached", nil, &one, 5, 3, 1, 0, true},
		{"per-customer reached", nil, nil, 1, 9, 9, 1, true},
		{"unlimited promotion and code", nil, nil, 3, 100, 100, 2, false},
	}
	for _, testCase := range cases {
		if got := PromoLimitReached(testCase.promotion, testCase.code, testCase.perCustomer, testCase.total, testCase.codeCount, testCase.mine); got != testCase.want {
			t.Fatalf("%s: got %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

// A goods price is a quote with an expiry. A payment that arrived after it
// lapsed — late by the window, or on a cancelled order whose quote has since
// lapsed — must not be delivered at that price.
func TestStaleGoodsQuoteFollowsTheQuoteExpiry(t *testing.T) {
	now := time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC)
	if !StaleGoodsQuote(ClassificationLate, now.Add(time.Hour), now) {
		t.Fatal("a late goods payment is stale")
	}
	if !StaleGoodsQuote(ClassificationPaidAfterCancellation, now.Add(-time.Second), now) {
		t.Fatal("a cancelled goods order paid after its quote lapsed is stale")
	}
	if StaleGoodsQuote(ClassificationPaidAfterCancellation, now.Add(time.Minute), now) {
		t.Fatal("a cancelled goods order paid while its quote holds is not stale")
	}
	if StaleGoodsQuote(ClassificationPaid, now.Add(-time.Hour), now) {
		t.Fatal("a payment the window accepted is not stale")
	}
}

// What keeps a matched payment from settling is a closed list. Underpayment
// and overpayment are absent on purpose: the order is already left unpaid by
// the domain, and a top-up settles them for what arrived.
func TestBlocksSettlementIsTheNonSettlingList(t *testing.T) {
	for _, classification := range []string{ClassificationCurrency, ClassificationWallet, ClassificationDuplicate, ClassificationSubscriptionUnavailable, ClassificationLimitExceeded, ClassificationStaleQuote} {
		if !BlocksSettlement(classification) || !NeedsOperator(classification) {
			t.Fatalf("%s must block settlement and reach an operator", classification)
		}
	}
	for _, classification := range []string{ClassificationPaid, ClassificationLate, ClassificationPaidAfterCancellation, ClassificationUnderpayment, ClassificationOverpayment, ClassificationReplay} {
		if BlocksSettlement(classification) {
			t.Fatalf("%s must not be blocked here", classification)
		}
	}
	if !ClosedUnpaid(OrderCancelled) || !ClosedUnpaid(OrderExpired) || ClosedUnpaid(OrderPending) || ClosedUnpaid(OrderPaid) {
		t.Fatal("only a cancelled or expired order closed unpaid")
	}
}
