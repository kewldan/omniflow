package goods

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNormalizeRecipientAcceptsTheFormsPeopleActuallyPaste(t *testing.T) {
	for _, input := range []string{
		"omniflow_user",
		"@omniflow_user",
		" @omniflow_user ",
		"t.me/omniflow_user",
		"https://t.me/omniflow_user",
		"https://t.me/omniflow_user?start=1",
		"telegram.me/omniflow_user/",
	} {
		got, err := NormalizeRecipient(input)
		if err != nil {
			t.Fatalf("NormalizeRecipient(%q): %v", input, err)
		}
		if got != "omniflow_user" {
			t.Fatalf("NormalizeRecipient(%q) = %q", input, got)
		}
	}
}

func TestNormalizeRecipientRefusesWhatTelegramWouldNotAccept(t *testing.T) {
	for _, input := range []string{
		"",
		"abc",                                  // shorter than five characters
		"1abcde",                               // must begin with a letter
		"has-a-dash",                           // dashes are not allowed
		"way_too_long_username_for_telegram_x", // longer than thirty-two
		"@",
	} {
		if _, err := NormalizeRecipient(input); !errors.Is(err, ErrInvalidRecipient) {
			t.Fatalf("NormalizeRecipient(%q) should have been refused", input)
		}
	}
}

func TestPriceAppliesMarkupThenRounding(t *testing.T) {
	// A two-decimal currency: 10.00 cost, 15% markup, rounded up to a whole unit.
	rule := PricingRule{Currency: "RUB", MarkupBPS: 1500, Rounding: RoundUnit}
	if got := Price(1000, rule, 2); got != 1200 {
		t.Fatalf("expected 1150 rounded up to 12.00, got %d", got)
	}

	// The markup itself rounds up rather than truncating, so a shop earns the
	// margin its operator configured.
	fine := PricingRule{Currency: "RUB", MarkupBPS: 1, Rounding: RoundNone}
	if got := Price(1000, fine, 2); got != 1001 {
		t.Fatalf("a 0.01%% markup on 1000 must round up to 1001, got %d", got)
	}

	// A zero-exponent currency: unit rounding is a no-op because a minor unit
	// already is a unit.
	stars := PricingRule{Currency: "XTR", MarkupBPS: 1000, Rounding: RoundUnit}
	if got := Price(100, stars, 0); got != 110 {
		t.Fatalf("expected 110 Stars, got %d", got)
	}
}

func TestPriceRoundingSteps(t *testing.T) {
	cases := []struct {
		rounding string
		want     int64
	}{
		{RoundNone, 10_001},
		{RoundMinor, 10_001},
		{RoundUnit, 10_100},
		{RoundTenUnits, 11_000},
		{RoundHundredUnits, 20_000},
	}
	for _, testCase := range cases {
		rule := PricingRule{Currency: "RUB", Rounding: testCase.rounding}
		if got := Price(10_001, rule, 2); got != testCase.want {
			t.Fatalf("rounding %q: got %d, want %d", testCase.rounding, got, testCase.want)
		}
	}

	// An amount already on the step is left alone rather than pushed to the next.
	exact := PricingRule{Currency: "RUB", Rounding: RoundUnit}
	if got := Price(10_000, exact, 2); got != 10_000 {
		t.Fatalf("an exact multiple must not be rounded up, got %d", got)
	}

	// An unrecognised mode behaves as `none` rather than inventing a price.
	future := PricingRule{Currency: "RUB", Rounding: "up_to_the_nearest_moon"}
	if got := Price(10_001, future, 2); got != 10_001 {
		t.Fatalf("an unknown rounding mode must not change the amount, got %d", got)
	}
}

func TestPriceHonoursAFixedOverride(t *testing.T) {
	fixed := int64(49_900)
	rule := PricingRule{Currency: "RUB", MarkupBPS: 5000, Rounding: RoundHundredUnits, FixedAmountMinor: &fixed}
	if got := Price(1_000_000, rule, 2); got != fixed {
		t.Fatalf("a fixed price must ignore both cost and markup, got %d", got)
	}
}

func TestQuoteFreshness(t *testing.T) {
	now := time.Now().UTC()
	if !(Quote{}).Fresh(now) {
		t.Fatal("an adapter that publishes no expiry must not be treated as stale")
	}
	if (Quote{ExpiresAt: now.Add(-time.Second)}).Fresh(now) {
		t.Fatal("an expired quote must not be fresh")
	}
	if !(Quote{ExpiresAt: now.Add(time.Minute)}).Fresh(now) {
		t.Fatal("a live quote must be fresh")
	}
}

func TestRetryAndRefundPartitionEveryFailureClass(t *testing.T) {
	classes := []string{
		FailureRetryable, FailurePermanent, FailureRecipientInvalid,
		FailureProviderBalance, FailureProviderUnavailable,
	}
	for _, class := range classes {
		retry, refund := Retryable(class), Refundable(class)
		if retry == refund {
			t.Fatalf("class %q is both retried (%v) and refunded (%v) or neither", class, retry, refund)
		}
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	if got := Backoff(0); got != time.Minute {
		t.Fatalf("a nonsensical attempt number must not produce a zero delay, got %v", got)
	}
	if got := Backoff(1); got != time.Minute {
		t.Fatalf("the first retry waits a minute, got %v", got)
	}
	if got := Backoff(3); got != 4*time.Minute {
		t.Fatalf("expected doubling, got %v", got)
	}
	if got := Backoff(MaxAttempts + 10); got != time.Hour {
		t.Fatalf("the schedule must be capped at an hour, got %v", got)
	}
}

// --- Fragment adapter -------------------------------------------------------

func fragmentServer(t *testing.T, handler http.HandlerFunc) *Fragment {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider, err := NewFragment(FragmentOptions{Token: "test-token", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestFragmentRequiresACredential(t *testing.T) {
	if _, err := NewFragment(FragmentOptions{}); err == nil {
		t.Fatal("an adapter with no token must not be constructed")
	}
}

func TestFragmentDeliverySendsTheIdempotencyKey(t *testing.T) {
	var seenKey, seenRecipient string
	provider := fragmentServer(t, func(writer http.ResponseWriter, request *http.Request) {
		seenKey = request.Header.Get("Idempotency-Key")
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		seenRecipient, _ = body["recipient"].(string)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"reference":"frg-1","status":"completed"}`))
	})

	delivery, err := provider.Deliver(context.Background(), DeliveryRequest{
		Request: Request{
			Kind: KindTelegramPremium, DurationMonths: 3, Quantity: 1,
			// Deliberately a pasted link: the adapter must normalise before
			// handing anything to the provider.
			Recipient: "https://t.me/@Omniflow_User",
		},
		OrderID: "order-1", IdempotencyKey: "goods:order-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if seenKey != "goods:order-1" {
		t.Fatalf("the idempotency key must reach the provider, got %q", seenKey)
	}
	if seenRecipient != "Omniflow_User" {
		t.Fatalf("the provider must receive a bare username, got %q", seenRecipient)
	}
	if delivery.Status != "delivered" || delivery.Reference != "frg-1" {
		t.Fatalf("unexpected delivery %+v", delivery)
	}
}

func TestFragmentClassifiesProviderFailures(t *testing.T) {
	cases := map[string]string{
		"recipient_not_found": FailureRecipientInvalid,
		"insufficient_funds":  FailureProviderBalance,
		"rate_limited":        FailureRetryable,
		"invalid_request":     FailurePermanent,
		"something_new":       FailureRetryable,
	}
	for code, want := range cases {
		provider := fragmentServer(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"status":"failed","error_code":"` + code + `"}`))
		})
		delivery, err := provider.Deliver(context.Background(), DeliveryRequest{
			Request: Request{Kind: KindTelegramStars, StarQuantity: 100, Recipient: "omniflow_user"},
			OrderID: "order", IdempotencyKey: "key",
		})
		if err != nil {
			t.Fatalf("a classified provider failure is a delivery outcome, not an error: %v", err)
		}
		if delivery.FailureClass != want {
			t.Fatalf("error code %q classified as %q, want %q", code, delivery.FailureClass, want)
		}
	}
}

func TestFragmentClassifiesTransportFailures(t *testing.T) {
	// A 4xx will not resolve on retry.
	refusing := fragmentServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "no", http.StatusBadRequest)
	})
	delivery, err := refusing.Deliver(context.Background(), DeliveryRequest{
		Request: Request{Kind: KindTelegramStars, StarQuantity: 50, Recipient: "omniflow_user"},
		OrderID: "order", IdempotencyKey: "key",
	})
	if err != nil || delivery.FailureClass != FailurePermanent {
		t.Fatalf("expected a permanent failure, got %+v (%v)", delivery, err)
	}

	// A 429 is a retry, not a refund.
	throttled := fragmentServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "slow down", http.StatusTooManyRequests)
	})
	delivery, _ = throttled.Deliver(context.Background(), DeliveryRequest{
		Request: Request{Kind: KindTelegramStars, StarQuantity: 50, Recipient: "omniflow_user"},
		OrderID: "order", IdempotencyKey: "key",
	})
	if delivery.FailureClass != FailureRetryable {
		t.Fatalf("throttling must be retryable, got %q", delivery.FailureClass)
	}

	// A 5xx is an outage: retried, and never assumed to have delivered nothing.
	broken := fragmentServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "oops", http.StatusBadGateway)
	})
	delivery, _ = broken.Deliver(context.Background(), DeliveryRequest{
		Request: Request{Kind: KindTelegramStars, StarQuantity: 50, Recipient: "omniflow_user"},
		OrderID: "order", IdempotencyKey: "key",
	})
	if delivery.FailureClass != FailureProviderUnavailable {
		t.Fatalf("an outage must be provider-unavailable, got %q", delivery.FailureClass)
	}
}

func TestFragmentRefusesAnUnreachableRecipientWithoutCallingTheProvider(t *testing.T) {
	called := false
	provider := fragmentServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		called = true
		writer.WriteHeader(http.StatusOK)
	})
	delivery, err := provider.Deliver(context.Background(), DeliveryRequest{
		Request: Request{Kind: KindTelegramStars, StarQuantity: 50, Recipient: "no"},
		OrderID: "order", IdempotencyKey: "key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("a username that cannot be a handle must not reach the provider")
	}
	if delivery.FailureClass != FailureRecipientInvalid {
		t.Fatalf("expected a recipient-invalid failure, got %+v", delivery)
	}
}

func TestFragmentQuoteRejectsAnUnusableAnswer(t *testing.T) {
	provider := fragmentServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"amount_minor":0,"currency":""}`))
	})
	if _, err := provider.Quote(context.Background(), Request{
		Kind: KindTelegramStars, StarQuantity: 100, Currency: "RUB",
	}); err == nil {
		t.Fatal("a quote with no amount must not be accepted")
	}
}

func TestFragmentValidateRecipientDoesNotBlockOnProviderOutage(t *testing.T) {
	provider := fragmentServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "down", http.StatusServiceUnavailable)
	})
	if err := provider.ValidateRecipient(context.Background(), "omniflow_user"); err != nil {
		t.Fatalf("a provider that cannot answer is not evidence of a bad username: %v", err)
	}

	missing := fragmentServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"exists":false}`))
	})
	if err := missing.ValidateRecipient(context.Background(), "omniflow_user"); !errors.Is(err, ErrInvalidRecipient) {
		t.Fatalf("expected ErrInvalidRecipient, got %v", err)
	}
}

func TestFragmentSupportsOnlyItsOwnKinds(t *testing.T) {
	provider, err := NewFragment(FragmentOptions{Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if !provider.Supports(KindTelegramPremium) || !provider.Supports(KindTelegramStars) {
		t.Fatal("Fragment sells both Telegram kinds")
	}
	if provider.Supports("gift_card") {
		t.Fatal("an unsupported kind must be refused rather than attempted")
	}
	if _, err := provider.Quote(context.Background(), Request{Kind: "gift_card"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}
