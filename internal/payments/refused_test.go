package payments

import (
	"errors"
	"fmt"
	"testing"
)

// A refusal is a request the provider turned down, so nothing moved. Anything
// that might have taken effect — an outage, a timeout, a conflict, a rate
// limit — is not one, because the only safe answer to it is to ask again.
func TestRefusedSeparatesARefusalFromAnUnknownOutcome(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 422} {
		if !Refused(fmt.Errorf("refund: %w", &StatusError{StatusCode: status})) {
			t.Fatalf("HTTP %d is a refusal", status)
		}
	}
	for _, status := range []int{408, 409, 429, 500, 502, 503} {
		if Refused(&StatusError{StatusCode: status}) {
			t.Fatalf("HTTP %d may have taken effect and is not a refusal", status)
		}
	}
	if !Refused(ErrUnsupported) {
		t.Fatal("an unsupported capability is a refusal")
	}
	if Refused(errors.New("call provider: context deadline exceeded")) {
		t.Fatal("a timeout is not a refusal")
	}
	if !errors.Is(&StatusError{StatusCode: 500}, ErrProviderResponse) {
		t.Fatal("a status error must still be a provider response error")
	}
}
