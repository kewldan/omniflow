//go:build integration

package integrationtest

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/fxrate"
	"github.com/omniflow/omniflow/internal/payments"
	"github.com/omniflow/omniflow/internal/paymentservice"
)

// A ruble top-up is paid in Stars at the operator's rate: the quote is fixed
// once, settling the quoted Stars credits the ruble amount, and a different
// Star count is converted at the quote's rate and classified, never credited
// as the full amount.
func (harness *harness) telegramCustomer(ctx context.Context, t *testing.T, subject string) string {
	t.Helper()
	customer := harness.customer(ctx, t)
	if _, err := harness.pool.Exec(ctx, `INSERT INTO identities (user_id, provider, provider_subject, verified_at, status)
		VALUES ($1::uuid, 'telegram', $2, now(), 'active')`, customer, subject); err != nil {
		t.Fatal(err)
	}
	return customer
}

func TestStarsPayARubleOrderAtTheQuotedRate(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	actor := harness.operator(ctx, t, "fx@omniflow.test")
	rates := fxrate.New(harness.pool, fxrate.Options{})
	if _, err := rates.SaveSettings(ctx, fxrate.Settings{StarsEnabled: true, StarsCurrency: "RUB", StarsRateMode: "manual",
		StarsRateMinor: 180, StarsUSDMicros: 13000, USDRUBMode: "auto"}, actor); err != nil {
		t.Fatal(err)
	}
	store := harness.commerceStore(t, testOptions())
	stars := (&payments.TelegramStars{}).WithConversion(fxrate.New(harness.pool, fxrate.Options{}).Convertible)
	paymentService := paymentservice.New(harness.pool, store, stars)
	checkout, err := accountcheckout.New(harness.pool, store, paymentService, accountcheckout.Options{
		Logger: slog.New(slog.DiscardHandler), Settings: accountcheckout.Settings{Currency: "RUB", PublicURL: "https://example.test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	customer := harness.telegramCustomer(ctx, t, "70001")
	topUp, err := checkout.StartTopUp(ctx, customer, "RUB", 18050, "telegram_stars", "fx-topup-0001")
	if err != nil {
		t.Fatalf("a ruble top-up through Stars was refused: %v", err)
	}
	quote, err := rates.QuoteOrder(ctx, topUp.OrderID)
	if err != nil || quote.Stars != 101 || quote.AmountMinor != 18050 {
		t.Fatalf("quote = %+v (%v)", quote, err)
	}
	// The rate changes; the quote does not.
	if _, err = harness.pool.Exec(ctx, `UPDATE currency_settings SET stars_rate_minor = 100`); err != nil {
		t.Fatal(err)
	}
	if again, _ := fxrate.New(harness.pool, fxrate.Options{}).QuoteOrder(ctx, topUp.OrderID); again.Stars != 101 {
		t.Fatalf("the quote moved with the rate: %+v", again)
	}

	// Too few Stars: converted at the quote's rate, so the order is not paid.
	classification, err := paymentService.SettleTelegramStars(ctx, paymentservice.StarsSettlement{
		OrderID: topUp.OrderID, ChargeID: "charge-short", AmountMinor: 50, UpdateID: 1,
	})
	if err == nil && classification == "exact" {
		t.Fatalf("an underpayment settled the order (%s)", classification)
	}
	if balance := harness.walletBalance(ctx, t, customer); balance == 18050 {
		t.Fatal("an underpayment credited the full amount")
	}

	other := harness.telegramCustomer(ctx, t, "70002")
	second, err := checkout.StartTopUp(ctx, other, "RUB", 18000, "telegram_stars", "fx-topup-0002")
	if err != nil {
		t.Fatal(err)
	}
	secondQuote, err := fxrate.New(harness.pool, fxrate.Options{}).QuoteOrder(ctx, second.OrderID)
	if err != nil || secondQuote.Stars != 180 || secondQuote.RateMinor != 100 {
		t.Fatalf("second quote = %+v (%v)", secondQuote, err)
	}
	if _, err = paymentService.SettleTelegramStars(ctx, paymentservice.StarsSettlement{
		OrderID: second.OrderID, ChargeID: "charge-exact", AmountMinor: secondQuote.Stars, UpdateID: 2,
	}); err != nil {
		t.Fatalf("settle the exact quote: %v", err)
	}
	if balance := harness.walletBalance(ctx, t, other); balance != 18000 {
		t.Fatalf("balance = %d, want the ruble amount", balance)
	}
}

// USD/RUB comes from the first source that answers, falls back to the stored
// reading while it is recent, and to the manual value after that.
func TestUSDRUBFallsBack(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	actor := harness.operator(ctx, t, "usd@omniflow.test")
	healthy := true
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !healthy {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if request.URL.Path == "/cbr" {
			writer.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = writer.Write([]byte(`{"result":"success","rates":{"RUB":91.5}}`))
	}))
	t.Cleanup(server.Close)
	now := time.Now()
	clock := func() time.Time { return now }
	options := fxrate.Options{CBRURL: server.URL + "/cbr", OpenURL: server.URL + "/open", Clock: clock}
	rates := fxrate.New(harness.pool, options)
	rate, err := rates.USDRUB(ctx)
	if err != nil || rate.Minor != 9150 || rate.Source != fxrate.SourceOpenER {
		t.Fatalf("rate = %+v (%v)", rate, err)
	}
	healthy = false
	restarted := fxrate.New(harness.pool, options)
	if rate, err = restarted.USDRUB(ctx); err != nil || rate.Source != fxrate.SourceStored || rate.Minor != 9150 {
		t.Fatalf("stored fallback = %+v (%v)", rate, err)
	}
	now = now.Add(72 * time.Hour)
	if _, err = fxrate.New(harness.pool, options).USDRUB(ctx); err == nil {
		t.Fatal("a three-day-old reading was used")
	}
	manual := int64(9000)
	if _, err = restarted.SaveSettings(ctx, fxrate.Settings{StarsRateMode: "auto", StarsCurrency: "RUB", StarsRateMinor: 180,
		StarsUSDMicros: 13000, USDRUBMode: "auto", USDRUBManualMinor: &manual, StarsEnabled: true}, actor); err != nil {
		t.Fatal(err)
	}
	fallback := fxrate.New(harness.pool, options)
	if rate, err = fallback.USDRUB(ctx); err != nil || rate.Source != fxrate.SourceManual || rate.Minor != 9000 {
		t.Fatalf("manual fallback = %+v (%v)", rate, err)
	}
	// The automatic Star price is USD/RUB times 0.013 USD, rounded up.
	if _, minor, ok, err := fallback.StarsRate(ctx); err != nil || !ok || minor != 117 {
		t.Fatalf("auto Stars rate = %d %v (%v)", minor, ok, err)
	}
}
