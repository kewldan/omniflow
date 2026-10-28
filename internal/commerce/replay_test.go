package commerce

import "testing"

// A success reported again for an intent that already settled its order is the
// same charge, not a second one. Only that pairing is a replay: an intent
// marked succeeded on an order still waiting for payment was never settled,
// and an order settled through another intent has been paid twice.
func TestSettlementReplayNeedsASucceededIntentAndASettledOrder(t *testing.T) {
	cases := []struct {
		intent string
		order  OrderState
		want   bool
	}{
		{"succeeded", OrderPaid, true},
		{"succeeded", OrderFulfilled, true},
		{"succeeded", OrderPartiallyRefunded, true},
		{"succeeded", OrderRefunded, true},
		{"succeeded", OrderPending, false},
		{"succeeded", OrderCancelled, false},
		{"succeeded", OrderExpired, false},
		{"processing", OrderPaid, false},
		{"requires_action", OrderFulfilled, false},
	}
	for _, testCase := range cases {
		if got := SettlementReplay(testCase.intent, testCase.order); got != testCase.want {
			t.Fatalf("SettlementReplay(%q, %q) = %v, want %v", testCase.intent, testCase.order, got, testCase.want)
		}
	}
	if NeedsOperator(ClassificationReplay) {
		t.Fatal("a replay must never reach an operator")
	}
}
