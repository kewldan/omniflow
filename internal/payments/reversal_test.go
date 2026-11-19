package payments

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIntentStatusNeverStoresAReversal(t *testing.T) {
	t.Parallel()
	for status, want := range map[string]string{
		StatusRefunded: "cancelled", StatusChargeback: "cancelled", "succeeded": "succeeded", "pending": "pending",
	} {
		if got := IntentStatus(status); got != want {
			t.Fatalf("IntentStatus(%q) = %q, want %q", status, got, want)
		}
	}
}

// A YooKassa refund made from the dashboard arrives as a refund object: it is
// routed to its payment, and the payment's authenticated read names the
// refunded total — a partial refund keeps the payment settled, a full one
// reports it refunded.
func TestYooKassaReportsProviderRefunds(t *testing.T) {
	t.Parallel()
	refunded := `"refunded_amount":{"value":"50.00","currency":"RUB"}`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"id":"p1","status":"succeeded","amount":{"value":"100.00","currency":"RUB"},`+refunded+`}`)
	}))
	t.Cleanup(server.Close)
	provider, _ := NewYooKassa("shop", "secret")
	provider.baseURL = server.URL

	event, err := provider.VerifyWebhook(nil, []byte(`{"type":"notification","event":"refund.succeeded",
		"object":{"id":"r1","payment_id":"p1","status":"succeeded","amount":{"value":"50.00","currency":"RUB"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if event.ProviderReference != "p1" {
		t.Fatalf("a refund notification is routed to %q, want its payment", event.ProviderReference)
	}
	partial, err := provider.Poll(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if partial.Status != "succeeded" || partial.RefundedMinor != 5000 {
		t.Fatalf("partial refund read as %q/%d", partial.Status, partial.RefundedMinor)
	}
	refunded = `"refunded_amount":{"value":"100.00","currency":"RUB"}`
	full, err := provider.Poll(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if full.Status != StatusRefunded || full.RefundedMinor != 10000 {
		t.Fatalf("full refund read as %q/%d", full.Status, full.RefundedMinor)
	}
}
