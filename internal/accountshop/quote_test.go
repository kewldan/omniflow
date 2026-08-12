package accountshop

import (
	"errors"
	"testing"
	"time"
)

// The quote a client submits is an assertion about what it displayed, and the
// only two ways it can fail have different remedies. Collapsing them would make
// the panel guess between re-quoting silently and asking the customer to agree
// to a new number.
func TestAQuoteMayOnlyBeChargedWhileItIsBothLiveAndUnchanged(t *testing.T) {
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	fresh := Quote{PriceMinor: 25_000, Currency: "RUB", ExpiresAt: now.Add(5 * time.Minute)}

	cases := []struct {
		name  string
		shown Quote
		want  error
	}{
		{
			name:  "the number on the screen is still the number quoted",
			shown: Quote{PriceMinor: 25_000, Currency: "RUB", ExpiresAt: now.Add(time.Minute)},
			want:  nil,
		},
		{
			name:  "the window closed while the customer was deciding",
			shown: Quote{PriceMinor: 25_000, Currency: "RUB", ExpiresAt: now.Add(-time.Second)},
			want:  ErrQuoteExpired,
		},
		{
			name:  "expiring exactly now is expired, because the promise was until then",
			shown: Quote{PriceMinor: 25_000, Currency: "RUB", ExpiresAt: now},
			want:  ErrQuoteExpired,
		},
		{
			name:  "no quote at all is the same condition as a stale one",
			shown: Quote{PriceMinor: 25_000, Currency: "RUB"},
			want:  ErrQuoteExpired,
		},
		{
			name:  "the rate moved inside the window",
			shown: Quote{PriceMinor: 24_000, Currency: "RUB", ExpiresAt: now.Add(time.Minute)},
			want:  ErrPriceChanged,
		},
		{
			name:  "a client claiming a lower price is refused rather than believed",
			shown: Quote{PriceMinor: 1, Currency: "RUB", ExpiresAt: now.Add(time.Minute)},
			want:  ErrPriceChanged,
		},
		{
			name:  "a client claiming another currency is refused",
			shown: Quote{PriceMinor: 25_000, Currency: "USD", ExpiresAt: now.Add(time.Minute)},
			want:  ErrPriceChanged,
		},
		{
			name:  "currency case is presentation, not a different currency",
			shown: Quote{PriceMinor: 25_000, Currency: "rub", ExpiresAt: now.Add(time.Minute)},
			want:  nil,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := evaluateQuote(testCase.shown, fresh, now); !errors.Is(err, testCase.want) {
				t.Fatalf("evaluateQuote = %v, want %v", err, testCase.want)
			}
		})
	}
}

// A forged expiry buys nothing: the charge is always the live number, so a
// client that invents a window still has to match the price the provider just
// gave.
func TestAForgedExpiryCannotCarryAStalePrice(t *testing.T) {
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	fresh := Quote{PriceMinor: 30_000, Currency: "RUB", ExpiresAt: now.Add(5 * time.Minute)}
	forged := Quote{PriceMinor: 25_000, Currency: "RUB", ExpiresAt: now.Add(100 * time.Hour)}

	if err := evaluateQuote(forged, fresh, now); !errors.Is(err, ErrPriceChanged) {
		t.Fatalf("a stale price with a far-future expiry was accepted: %v", err)
	}
}

func TestQuantityIsClampedRatherThanTrusted(t *testing.T) {
	cases := []struct {
		given, want int
	}{
		{given: 0, want: 1},
		{given: 1, want: 1},
		{given: maxQuantity, want: maxQuantity},
		{given: maxQuantity + 1, want: 0},
		{given: -1, want: 0},
		{given: 1 << 30, want: 0},
	}
	for _, testCase := range cases {
		if got := normalizeQuantity(testCase.given); got != testCase.want {
			t.Fatalf("normalizeQuantity(%d) = %d, want %d", testCase.given, got, testCase.want)
		}
	}
}

// An unknown currency must not scale a price. Falling back to a zero exponent
// loses the unit-rounding step, which is a smaller mistake than multiplying
// somebody's bill by a hundred.
func TestAnUnknownCurrencyHasNoExponent(t *testing.T) {
	if got := currencyExponent("RUB"); got != 2 {
		t.Fatalf("currencyExponent(RUB) = %d, want 2", got)
	}
	if got := currencyExponent("XTR"); got != 0 {
		t.Fatalf("currencyExponent(XTR) = %d, want 0", got)
	}
	if got := currencyExponent("ZZZ"); got != 0 {
		t.Fatalf("currencyExponent(ZZZ) = %d, want 0", got)
	}
}
