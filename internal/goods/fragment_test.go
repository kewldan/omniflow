package goods

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func gateway(t *testing.T, handler http.HandlerFunc) *Fragment {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider, err := NewFragment(FragmentOptions{
		BaseURL: server.URL, Token: "test-token", Currency: "RUB",
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func starsRequest() DeliveryRequest {
	return DeliveryRequest{
		Request:         Request{Kind: KindTelegramStars, StarQuantity: 100, Recipient: "omniflow_user"},
		OrderID:         "order-1",
		IdempotencyKey:  "goods:order-1",
		BuyerTelegramID: 4242,
	}
}

func TestNewFragmentRefusesAnUnusableConfiguration(t *testing.T) {
	for name, options := range map[string]FragmentOptions{
		"no url":       {Token: "t"},
		"no token":     {BaseURL: "https://gateway.example"},
		"relative url": {BaseURL: "gateway.example", Token: "t"},
	} {
		if _, err := NewFragment(options); err == nil {
			t.Fatalf("%s: the adapter must not be constructed", name)
		}
	}
}

func TestFragmentAuthenticatesAsTheOmniflowUser(t *testing.T) {
	var user, password string
	var ok bool
	provider := gateway(t, func(writer http.ResponseWriter, request *http.Request) {
		user, password, ok = request.BasicAuth()
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	})

	if _, err := provider.Deliver(context.Background(), starsRequest()); err != nil {
		t.Fatal(err)
	}
	if !ok || user != "omniflow" || password != "test-token" {
		t.Fatalf("expected HTTP Basic omniflow/test-token, got %q/%q (ok=%v)", user, password, ok)
	}
}

func TestFragmentDeliversStarsToTheUsername(t *testing.T) {
	var path string
	var body map[string]any
	provider := gateway(t, func(writer http.ResponseWriter, request *http.Request) {
		path = request.URL.Path
		_ = json.NewDecoder(request.Body).Decode(&body)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	})

	request := starsRequest()
	// Deliberately a pasted link: the adapter must normalise before handing
	// anything to the gateway.
	request.Recipient = "https://t.me/@Omniflow_User"

	delivery, err := provider.Deliver(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/admin/stars/send" {
		t.Fatalf("unexpected path %q", path)
	}
	if body["username"] != "Omniflow_User" {
		t.Fatalf("the gateway must receive a bare username, got %v", body["username"])
	}
	if body["quantity"] != float64(100) {
		t.Fatalf("unexpected quantity %v", body["quantity"])
	}
	// Delivery is addressed by username; the identifier is the buyer's, because
	// Omniflow does not know the numeric identifier behind an arbitrary handle.
	if body["user_id"] != float64(4242) {
		t.Fatalf("expected the buyer's Telegram identifier, got %v", body["user_id"])
	}
	if delivery.Status != "delivered" {
		t.Fatalf("unexpected delivery %+v", delivery)
	}
}

func TestFragmentDeliversPremiumByDuration(t *testing.T) {
	var path string
	var body map[string]any
	provider := gateway(t, func(writer http.ResponseWriter, request *http.Request) {
		path = request.URL.Path
		_ = json.NewDecoder(request.Body).Decode(&body)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	})

	_, err := provider.Deliver(context.Background(), DeliveryRequest{
		Request:         Request{Kind: KindTelegramPremium, DurationMonths: 6, Recipient: "omniflow_user"},
		OrderID:         "order-2",
		BuyerTelegramID: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/admin/premium/send" {
		t.Fatalf("unexpected path %q", path)
	}
	if body["quantity"] != float64(6) {
		t.Fatalf("Premium is submitted as a month count, got %v", body["quantity"])
	}
}

// The gateway enforces Fragment's own floors. A product configured outside them
// can never be delivered, so the customer is refunded rather than made to wait
// through a retry schedule that cannot succeed.
func TestFragmentRefusesQuantitiesTheGatewayWillNotAccept(t *testing.T) {
	called := false
	provider := gateway(t, func(writer http.ResponseWriter, _ *http.Request) {
		called = true
		writer.WriteHeader(http.StatusOK)
	})

	cases := map[string]Request{
		"stars below the floor": {Kind: KindTelegramStars, StarQuantity: 10, Recipient: "omniflow_user"},
		"unsold premium term":   {Kind: KindTelegramPremium, DurationMonths: 1, Recipient: "omniflow_user"},
	}
	for name, request := range cases {
		delivery, err := provider.Deliver(context.Background(), DeliveryRequest{Request: request})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if delivery.FailureClass != FailurePermanent {
			t.Fatalf("%s: expected a permanent failure, got %+v", name, delivery)
		}
	}
	if called {
		t.Fatal("a request the gateway would refuse must not be submitted at all")
	}
}

func TestFragmentRefusesAnUnusableRecipientWithoutCallingTheGateway(t *testing.T) {
	called := false
	provider := gateway(t, func(writer http.ResponseWriter, _ *http.Request) {
		called = true
		writer.WriteHeader(http.StatusOK)
	})

	request := starsRequest()
	request.Recipient = "no"
	delivery, err := provider.Deliver(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("a username that cannot be a handle must not reach the gateway")
	}
	if delivery.FailureClass != FailureRecipientInvalid {
		t.Fatalf("expected a recipient-invalid failure, got %+v", delivery)
	}
}

// The gateway settles on-chain before it answers and writes its own records
// afterwards, so a server error can be raised from code that runs *after* the
// goods were paid for. Neither retrying nor refunding is safe.
func TestFragmentTreatsServerErrorsAsAmbiguous(t *testing.T) {
	provider := gateway(t, func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "order processing failed", http.StatusInternalServerError)
	})

	delivery, err := provider.Deliver(context.Background(), starsRequest())
	if err != nil {
		t.Fatal(err)
	}
	if delivery.FailureClass != FailureAmbiguous {
		t.Fatalf("expected an ambiguous outcome, got %+v", delivery)
	}
	if Retryable(delivery.FailureClass) || Refundable(delivery.FailureClass) {
		t.Fatal("an ambiguous outcome must be resolved by a person, not automatically")
	}
}

// An answer that never arrives is the same unknown: the request may never have
// been received, or its response may have been lost after the gateway acted.
func TestFragmentTreatsAnUnreachableGatewayAsAmbiguous(t *testing.T) {
	provider, err := NewFragment(FragmentOptions{
		// A port nothing listens on, so the request fails at the transport.
		BaseURL: "http://127.0.0.1:1", Token: "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	delivery, deliverErr := provider.Deliver(context.Background(), starsRequest())
	if deliverErr != nil {
		t.Fatal(deliverErr)
	}
	if delivery.FailureClass != FailureAmbiguous {
		t.Fatalf("expected an ambiguous outcome, got %+v", delivery)
	}
}

func TestFragmentClassifiesTheOtherTransportOutcomes(t *testing.T) {
	cases := map[int]string{
		http.StatusTooManyRequests: FailureRetryable,
		// A rejected credential is an operator configuration problem, so a
		// corrected token should let the delivery through rather than having
		// refunded the customer in the meantime.
		http.StatusUnauthorized: FailureProviderUnavailable,
		http.StatusForbidden:    FailureProviderUnavailable,
		http.StatusBadRequest:   FailurePermanent,
	}
	for status, want := range cases {
		provider := gateway(t, func(writer http.ResponseWriter, _ *http.Request) {
			http.Error(writer, "no", status)
		})
		delivery, err := provider.Deliver(context.Background(), starsRequest())
		if err != nil {
			t.Fatalf("HTTP %d: %v", status, err)
		}
		if delivery.FailureClass != want {
			t.Fatalf("HTTP %d classified as %q, want %q", status, delivery.FailureClass, want)
		}
	}
}

func TestFragmentQuotesStarsFromThePublishedValue(t *testing.T) {
	provider := gateway(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/admin/stars/value" {
			t.Errorf("unexpected path %q", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true,"value":1.18}`))
	})

	quote, err := provider.Quote(context.Background(), Request{
		Kind: KindTelegramStars, StarQuantity: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 1.18 per star, rounded to the minor unit and then multiplied, which is
	// the arithmetic the gateway itself charges against.
	if quote.CostMinor != 11_800 {
		t.Fatalf("CostMinor = %d, want 11800", quote.CostMinor)
	}
	if quote.Currency != "RUB" {
		t.Fatalf("Currency = %q", quote.Currency)
	}
}

// Premium prices live in the gateway's own table and are not published, so the
// operator sets the sale price in the panel and the margin on such an order is
// unknown rather than zero.
func TestFragmentReportsPremiumCostAsUnavailable(t *testing.T) {
	provider := gateway(t, func(writer http.ResponseWriter, _ *http.Request) {
		t.Error("no cost endpoint should be called for Premium")
		writer.WriteHeader(http.StatusOK)
	})

	_, err := provider.Quote(context.Background(), Request{
		Kind: KindTelegramPremium, DurationMonths: 3,
	})
	if !errors.Is(err, ErrCostUnavailable) {
		t.Fatalf("expected ErrCostUnavailable, got %v", err)
	}
}

func TestFragmentRejectsAnUnusableStarValue(t *testing.T) {
	provider := gateway(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":false,"value":0}`))
	})
	if _, err := provider.Quote(context.Background(), Request{
		Kind: KindTelegramStars, StarQuantity: 100,
	}); err == nil {
		t.Fatal("a star value of zero must not be accepted as a quote")
	}
}

// The gateway exposes neither a reference to poll nor its wallet balance, and
// saying so explicitly is what stops the delivery worker waiting for a signal
// that will never come.
func TestFragmentReportsItsMissingCapabilities(t *testing.T) {
	provider, err := NewFragment(FragmentOptions{BaseURL: "https://gateway.example", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Poll(context.Background(), "reference"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Poll should report ErrUnsupported, got %v", err)
	}
	if _, _, err := provider.Balance(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Balance should report ErrUnsupported, got %v", err)
	}
	if !provider.Supports(KindTelegramPremium) || !provider.Supports(KindTelegramStars) {
		t.Fatal("the gateway sells both Telegram kinds")
	}
	if provider.Supports("gift_card") {
		t.Fatal("an unsupported kind must be refused rather than attempted")
	}
}

// The adapter must satisfy the provider contract; it deliberately does not
// implement RecipientValidator, because the gateway exposes no check endpoint.
var _ Provider = (*Fragment)(nil)
