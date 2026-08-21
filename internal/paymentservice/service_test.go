package paymentservice

import "testing"

// The event a synchronous settlement writes is keyed on the provider's
// reference so it reads as the charge it records, and on the intent when the
// provider returned none — never on an empty string, which the payment-event
// uniqueness would treat as one shared event across every such charge.
func TestSynchronousEventIDPrefersTheProviderReference(t *testing.T) {
	if got := SynchronousEventID("pay-123", "intent-1"); got != "charge:pay-123" {
		t.Fatalf("with a reference: %q", got)
	}
	if got := SynchronousEventID("", "intent-1"); got != "charge:intent-1" {
		t.Fatalf("without a reference: %q", got)
	}
}
