package botapp

import (
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
)

func TestParseStartPayloadRecognisesEveryDeepLink(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want startPayload
	}{
		{"bare start", "/start", startPayload{}},
		{"menu", "/menu", startPayload{}},
		{"referral", "/start ref_ABCDEFGHIJ", startPayload{Kind: startPayloadReferral, Value: "ABCDEFGHIJ"}},
		{"empty referral", "/start ref_", startPayload{}},
		{"login", "/start login", startPayload{Kind: startPayloadLogin}},
		{"login is exact", "/start login_now", startPayload{}},
		{"pay", "/start pay_0c3b1a8e-9d4f-4b2a-8c1e-5f6a7b8c9d0e", startPayload{Kind: startPayloadPay, Value: "0c3b1a8e-9d4f-4b2a-8c1e-5f6a7b8c9d0e"}},
		{"pay normalises case", "/start pay_0C3B1A8E-9D4F-4B2A-8C1E-5F6A7B8C9D0E", startPayload{Kind: startPayloadPay, Value: "0c3b1a8e-9d4f-4b2a-8c1e-5f6a7b8c9d0e"}},
		{"pay needs a uuid", "/start pay_not-an-order", startPayload{}},
		{"pay needs a value", "/start pay_", startPayload{}},
		{"unknown payload", "/start campaign42", startPayload{}},
		{"two arguments", "/start ref_A ref_B", startPayload{}},
		{"surrounding whitespace", "  /start   login  ", startPayload{Kind: startPayloadLogin}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := parseStartPayload(tc.text); got != tc.want {
				t.Fatalf("parseStartPayload(%q) = %+v, want %+v", tc.text, got, tc.want)
			}
		})
	}
}

func TestStarsInvoiceDueOnlyForOpenUnsettledStarsOrders(t *testing.T) {
	t.Parallel()
	due := OrderSummary{State: commerce.OrderPending, Currency: "XTR", ExternalMinor: 250}
	if !starsInvoiceDue(due) {
		t.Fatal("an open Stars order with an amount left must get an invoice")
	}
	draft := due
	draft.State = commerce.OrderDraft
	if !starsInvoiceDue(draft) {
		t.Fatal("a draft order is still open")
	}
	for name, mutate := range map[string]func(*OrderSummary){
		"paid":          func(o *OrderSummary) { o.State = commerce.OrderPaid },
		"cancelled":     func(o *OrderSummary) { o.State = commerce.OrderCancelled },
		"expired":       func(o *OrderSummary) { o.State = commerce.OrderExpired },
		"not stars":     func(o *OrderSummary) { o.Currency = "RUB" },
		"wallet funded": func(o *OrderSummary) { o.ExternalMinor = 0 },
		"settled":       func(o *OrderSummary) { o.PaymentStatus = "succeeded" },
	} {
		order := due
		mutate(&order)
		if starsInvoiceDue(order) {
			t.Fatalf("%s: no invoice may be sent", name)
		}
	}
}
