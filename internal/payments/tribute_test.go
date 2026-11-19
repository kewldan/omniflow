package payments

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
)

// tributeTestSign is hex(HMAC-SHA256("tribute-api-key", body)) for
// tributeTestBody, computed the way the reference integration verifies it.
const (
	tributeTestBody = `{"name":"new_donation","created_at":"2026-07-27T16:23:22Z","sent_at":"2026-07-27T16:23:23Z","payload":{"donation_request_id":156031,"donation_name":"Top up","period":"once","amount":15000,"currency":"rub","telegram_user_id":111,"trb_user_id":"trb_abc"}}`
	tributeTestSign = "0c8907d9de1451d698932bd62aaa0dbbc21a6cd0ff1f4a16eb210b08cf96d878"
)

func tributeTestProvider(t *testing.T) *Tribute {
	t.Helper()
	built, err := buildTribute(Settings{Env: func(key string) string {
		return map[string]string{"APP_TRIBUTE_API_KEY": "tribute-api-key", "APP_TRIBUTE_DONATE_LINK": "https://t.me/tribute/app?startapp=dXYZ"}[key]
	}})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return built.(*Tribute)
}

func tributeTestHeaders(sign string) http.Header {
	return http.Header{"Trbt-Signature": {sign}}
}

func TestTributeBuilder(t *testing.T) {
	t.Parallel()
	settings := func(values map[string]string) Settings {
		return Settings{Env: func(key string) string { return values[key] }}
	}
	if _, err := buildTribute(settings(nil)); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no credentials must be ErrNotConfigured, got %v", err)
	}
	for _, partial := range []map[string]string{{"APP_TRIBUTE_API_KEY": "k"}, {"APP_TRIBUTE_DONATE_LINK": "https://t.me/x"}} {
		if _, err := buildTribute(settings(partial)); err == nil || errors.Is(err, ErrNotConfigured) {
			t.Fatalf("%v: half a configuration must be a startup error, got %v", partial, err)
		}
	}
	provider := tributeTestProvider(t)
	caps := provider.Capabilities()
	if provider.Name() != "tribute" || !caps.Webhooks || caps.Polling || caps.Refunds || caps.Recurring {
		t.Fatalf("unexpected capabilities %+v", caps)
	}
}

func TestTributeCreateHandsOutTheDonationLink(t *testing.T) {
	t.Parallel()
	provider := tributeTestProvider(t)
	intent, err := provider.Create(context.Background(), CreateRequest{OrderID: "o", Amount: commerce.Money{Amount: 15000, Currency: "RUB"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if intent.CheckoutURL != "https://t.me/tribute/app?startapp=dXYZ" || intent.Status != "requires_action" || intent.ProviderReference != "" {
		t.Fatalf("unexpected intent %+v", intent)
	}
	if _, err := provider.Create(context.Background(), CreateRequest{Amount: commerce.Money{Amount: 1, Currency: "USD"}}); err == nil {
		t.Fatal("a non-RUB order must be refused")
	}
	if _, err := provider.Poll(context.Background(), "x"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Tribute has no status API, got %v", err)
	}
	if _, err := provider.Refund(context.Background(), RefundRequest{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("refunds are not ported, got %v", err)
	}
}

func TestTributeWebhookMatchesTheReferenceSignature(t *testing.T) {
	t.Parallel()
	provider := tributeTestProvider(t)
	event, err := provider.VerifyWebhook(tributeTestHeaders(tributeTestSign), []byte(tributeTestBody))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if event.Status != "succeeded" || event.Amount != (commerce.Money{Amount: 15000, Currency: "RUB"}) ||
		event.ProviderReference != "156031_111_15000_2026-07-27T16:23:22Z" || event.MerchantReference != "" {
		t.Fatalf("unexpected event %+v", event)
	}
	repeat, _ := provider.VerifyWebhook(tributeTestHeaders(tributeTestSign), []byte(tributeTestBody))
	if repeat.ID != event.ID {
		t.Fatalf("a redelivery must keep its ID: %q vs %q", repeat.ID, event.ID)
	}
	// A second donation through the same link is a different event.
	other := strings.Replace(tributeTestBody, "16:23:22Z", "16:30:00Z", 1)
	otherEvent, err := provider.VerifyWebhook(tributeTestHeaders(tributeSignFor(other)), []byte(other))
	if err != nil || otherEvent.ID == event.ID {
		t.Fatalf("distinct donations must not share an ID: %v", err)
	}
}

func TestTributeWebhookRejectsForgeries(t *testing.T) {
	t.Parallel()
	provider := tributeTestProvider(t)
	tampered := strings.Replace(tributeTestBody, `"amount":15000`, `"amount":150000`, 1)
	for name, check := range map[string]struct {
		headers http.Header
		body    string
	}{
		"tampered amount": {tributeTestHeaders(tributeTestSign), tampered},
		"wrong signature": {tributeTestHeaders(strings.Repeat("0", 64)), tributeTestBody},
		"not hex":         {tributeTestHeaders("zz"), tributeTestBody},
		"no signature":    {http.Header{}, tributeTestBody},
	} {
		if _, err := provider.VerifyWebhook(check.headers, []byte(check.body)); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("%s: expected a signature rejection, got %v", name, err)
		}
	}
	for body, want := range map[string]string{
		`{"name":"cancelled_subscription","created_at":"2026-07-27T16:23:22Z","payload":{"amount":15000,"currency":"rub"}}`: "cancelled",
		`{"name":"something_else","created_at":"2026-07-27T16:23:22Z","payload":{}}`:                                        "pending",
	} {
		event, err := provider.VerifyWebhook(tributeTestHeaders(tributeSignFor(body)), []byte(body))
		if err != nil || event.Status != want {
			t.Fatalf("%s: got %+v %v, want %s", body, event, err, want)
		}
	}
	noAmount := `{"name":"new_donation","created_at":"2026-07-27T16:23:22Z","payload":{"telegram_user_id":1}}`
	if _, err := provider.VerifyWebhook(tributeTestHeaders(tributeSignFor(noAmount)), []byte(noAmount)); err == nil {
		t.Fatal("a donation without an amount must be rejected")
	}
}

func tributeSignFor(body string) string {
	mac := hmac.New(sha256.New, []byte("tribute-api-key"))
	_, _ = mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}
