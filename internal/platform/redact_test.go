package platform

import (
	"strings"
	"testing"
)

// The values below are synthetic. A real token, link, or card number must never
// appear in a test fixture either.
func TestRedactRemovesSecretsFromFreeText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
		leak  string
	}{
		{"telegram bot token", "failed with token 123456789:AAFakeTokenValueThatIsLongEnough00", "AAFakeTokenValueThatIsLongEnough00"},
		{"bearer header", `Authorization: Bearer abcdefghijklmnop`, "abcdefghijklmnop"},
		{"subscription link", "open https://panel.example.com/sub/abc123token", "abc123token"},
		{"vless link", "vless://uuid@host:443?security=reality#name", "uuid@host"},
		{"card-like digits", "reference 4111111111111111", "4111111111111111"},
		{"hwid", "hwid=ABCDEF0123456789", "ABCDEF0123456789"},
		{"api key", `api_key: "sk-live-0123456789abcdef"`, "sk-live-0123456789abcdef"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			redacted := Redact(testCase.input)
			if strings.Contains(redacted, testCase.leak) {
				t.Fatalf("%s survived redaction: %s", testCase.leak, redacted)
			}
			if !strings.Contains(redacted, Redacted) {
				t.Fatalf("expected a redaction marker in %q", redacted)
			}
		})
	}
}

func TestRedactLeavesOrdinaryTextAlone(t *testing.T) {
	t.Parallel()
	message := "order rejected: plan_unavailable"
	if got := Redact(message); got != message {
		t.Fatalf("ordinary text must survive unchanged, got %q", got)
	}
	if got := Redact(""); got != "" {
		t.Fatalf("an empty string must stay empty, got %q", got)
	}
}

func TestRedactURLKeepsOnlyTheHost(t *testing.T) {
	t.Parallel()
	got := RedactURL("https://user:secret@panel.example.com/api/users?token=abc")
	if strings.Contains(got, "secret") || strings.Contains(got, "abc") || strings.Contains(got, "api/users") {
		t.Fatalf("credentials, path, and query must all be dropped: %s", got)
	}
	if !strings.Contains(got, "panel.example.com") {
		t.Fatalf("the host is what makes the value useful: %s", got)
	}
	if got := RedactURL("not a url"); got != Redacted {
		t.Fatalf("an unparsable value must be fully redacted, got %q", got)
	}
}

func TestRedactFieldsRedactsBySensitiveKeyAndByValue(t *testing.T) {
	t.Parallel()
	safe := RedactFields(map[string]any{
		"orderId":         "018f-1234",
		"amountMinor":     int64(1000),
		"subscriptionUrl": "https://panel.example.com/sub/x",
		"note":            "call 4111111111111111 back",
		"nested":          map[string]any{"token": "abcdefgh12345678", "state": "paid"},
	})
	if safe["orderId"] != "018f-1234" || safe["amountMinor"] != int64(1000) {
		t.Fatalf("non-sensitive fields must survive: %v", safe)
	}
	if safe["subscriptionUrl"] != Redacted {
		t.Fatalf("a sensitive key must be replaced wholesale: %v", safe["subscriptionUrl"])
	}
	if note, _ := safe["note"].(string); strings.Contains(note, "4111111111111111") {
		t.Fatalf("a sensitive value must be redacted even under a safe key: %q", note)
	}
	nested, ok := safe["nested"].(map[string]any)
	if !ok || nested["token"] != Redacted || nested["state"] != "paid" {
		t.Fatalf("nested payloads must be redacted recursively: %v", safe["nested"])
	}
	if RedactFields(nil) != nil {
		t.Fatal("a nil payload must stay nil")
	}
}
