package httpapi

import (
	"net/http/httptest"
	"net/url"
	"testing"
)

// The panel percent-encodes a reason because a browser cannot send a Russian
// one in a header at all; the API decodes it, and keeps a value sent the old
// way, or one that does not decode, exactly as it came.
func TestOperatorReasonDecodesThePanelsEncoding(t *testing.T) {
	cases := []struct {
		name, value, encoding, want string
	}{
		{"encoded Russian", url.PathEscape("Нарушение правил: мультиаккаунт"), "percent", "Нарушение правил: мультиаккаунт"},
		{"encoded with a percent sign", url.PathEscape("50% refund"), "percent", "50% refund"},
		{"plain ASCII, no encoding header", "customer card", "", "customer card"},
		{"plain value with a percent sign", "50% refund", "", "50% refund"},
		{"undecodable value keeps what was sent", "50%zz", "percent", "50%zz"},
		{"surrounding space is trimmed", url.PathEscape("  причина  "), "percent", "причина"},
	}
	for _, test := range cases {
		request := httptest.NewRequest("POST", "/", nil)
		request.Header.Set("X-Operator-Reason", test.value)
		if test.encoding != "" {
			request.Header.Set("X-Operator-Reason-Encoding", test.encoding)
		}
		if got := operatorReason(request); got != test.want {
			t.Errorf("%s: got %q, want %q", test.name, got, test.want)
		}
	}
}
