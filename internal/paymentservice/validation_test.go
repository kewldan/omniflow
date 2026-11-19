package paymentservice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/payments"
)

// Every refusal below happens before the database is touched. The service is
// built with a nil pool, so reaching it would panic and fail the test: a
// malformed money request must never get as far as a lock or a row.

func TestValidIdempotencyKeyBounds(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"", false},
		{"1234567", false},
		{"12345678", true},
		{strings.Repeat("k", 128), true},
		{strings.Repeat("k", 129), false},
	}
	for _, testCase := range cases {
		if got := validIdempotencyKey(testCase.key); got != testCase.want {
			t.Fatalf("validIdempotencyKey(len %d) = %v, want %v", len(testCase.key), got, testCase.want)
		}
	}
}

func TestRefundRefusesMalformedRequestsBeforeAnyDatabaseWork(t *testing.T) {
	service := New(nil, nil)
	ctx := context.Background()
	money := commerce.Money{Amount: 100, Currency: "RUB"}
	const intent = "0b6f6f0e-6f6c-4c1a-9a5e-3f1d2f0c9a11"
	cases := []struct {
		name   string
		intent string
		key    string
		reason string
	}{
		{"short key", intent, "short", "duplicate charge"},
		{"empty key", intent, "", "duplicate charge"},
		{"overlong key", intent, strings.Repeat("k", 129), "duplicate charge"},
		{"no reason", intent, "refund-key-1", ""},
		{"malformed intent", "not-a-uuid", "refund-key-1", "duplicate charge"},
		{"empty intent", "", "refund-key-1", "duplicate charge"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := service.Refund(ctx, testCase.intent, testCase.key, money, testCase.reason, nil); err == nil {
				t.Fatal("the refund was accepted")
			}
		})
	}
}

func TestManualApprovalRefusesAnUnattributedDecision(t *testing.T) {
	service := New(nil, nil)
	ctx := context.Background()
	const intent = "0b6f6f0e-6f6c-4c1a-9a5e-3f1d2f0c9a11"
	cases := []struct {
		name, intent, operator, reason, key string
	}{
		{"no operator", intent, "", "bank transfer seen", "approve-key-1"},
		{"no reason", intent, "operator", "", "approve-key-1"},
		{"short key", intent, "operator", "bank transfer seen", "key"},
		{"malformed intent", "intent-1", "operator", "bank transfer seen", "approve-key-1"},
	}
	for _, testCase := range cases {
		for _, approved := range []bool{true, false} {
			if _, err := service.ApproveManual(ctx, testCase.intent, testCase.operator, testCase.reason,
				testCase.key, "request", approved); err == nil {
				t.Fatalf("%s (approved=%v): the decision was accepted", testCase.name, approved)
			}
		}
	}
}

func TestCreateIntentRefusesAnUnknownProviderOrKey(t *testing.T) {
	service := New(nil, nil)
	ctx := context.Background()
	if _, err := service.CreateIntent(ctx, CreateIntentInput{IdempotencyKey: "short", Provider: "manual"}); err == nil {
		t.Fatal("a short idempotency key was accepted")
	}
	if _, err := service.CreateIntent(ctx, CreateIntentInput{IdempotencyKey: "intent-key-1", Provider: "yookassa"}); err == nil {
		t.Fatal("an unconfigured provider was accepted")
	}
	if service.Enabled("yookassa") || service.Enabled("") {
		t.Fatal("a provider that was never configured reports enabled")
	}
}

// A webhook for a provider this installation does not run is refused before
// it is verified or stored, so an anonymous caller cannot fill the events
// table by naming providers that do not exist.
func TestWebhookForAnUnconfiguredProviderIsUnsupported(t *testing.T) {
	service := New(nil, nil)
	for _, provider := range []string{"yookassa", "cryptobot", "", "../manual"} {
		if _, err := service.HandleWebhook(context.Background(), provider, http.Header{}, []byte(`{}`)); !errors.Is(err, payments.ErrUnsupported) {
			t.Fatalf("HandleWebhook(%q) = %v, want ErrUnsupported", provider, err)
		}
	}
	if _, err := service.SettleTelegramStars(context.Background(), StarsSettlement{
		OrderID: "0b6f6f0e-6f6c-4c1a-9a5e-3f1d2f0c9a11", ChargeID: "charge", AmountMinor: 50,
	}); !errors.Is(err, payments.ErrUnsupported) {
		t.Fatalf("SettleTelegramStars without the adapter = %v, want ErrUnsupported", err)
	}
}

// The headers stored beside a webhook are an allowlist. A provider signature,
// an Authorization header, or a cookie must never land in the events table.
func TestSafeHeadersKeepsOnlyTheAllowlist(t *testing.T) {
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	headers.Set("User-Agent", "YooKassa")
	headers.Set("Authorization", "Basic c2hvcDpzZWNyZXQ=")
	headers.Set("Crypto-Pay-Api-Signature", "deadbeef")
	headers.Set("Cookie", "session=abc")
	headers.Set("X-Forwarded-For", "203.0.113.9")

	var stored map[string]string
	if err := json.Unmarshal(safeHeaders(headers), &stored); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(stored) != 2 || stored["content-type"] != "application/json" || stored["user-agent"] != "YooKassa" {
		t.Fatalf("stored headers = %v", stored)
	}
	encoded := string(safeHeaders(headers))
	for _, secret := range []string{"c2hvcDpzZWNyZXQ", "deadbeef", "session=abc", "203.0.113.9"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("stored headers carry %q", secret)
		}
	}
}

func TestUUIDRoundTrip(t *testing.T) {
	const canonical = "0b6f6f0e-6f6c-4c1a-9a5e-3f1d2f0c9a11"
	for _, input := range []string{canonical, strings.ToUpper(canonical)} {
		id, err := parseUUID(input)
		if err != nil {
			t.Fatalf("parseUUID(%q): %v", input, err)
		}
		if got := uuidString(id); got != canonical {
			t.Fatalf("uuidString = %q, want %q", got, canonical)
		}
	}
	for _, input := range []string{"", "0b6f6f0e", "0b6f6f0e-6f6c-4c1a-9a5e-3f1d2f0c9a1z", "' OR 1=1 --"} {
		if _, err := parseUUID(input); err == nil {
			t.Fatalf("parseUUID(%q) was accepted", input)
		}
	}
}
