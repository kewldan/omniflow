package tonrate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestManualOverrideWins(t *testing.T) {
	service := New(Options{Currency: "RUB", Exponent: 2, ManualMinorPerTON: 25_000,
		TonapiURL: "http://127.0.0.1:1", CoinGeckoURL: "http://127.0.0.1:1"})
	rate, err := service.Current(context.Background())
	if err != nil || rate != (Rate{MinorPerTON: 25_000, Currency: "RUB", Source: SourceManual}) {
		t.Fatalf("rate = %+v, %v", rate, err)
	}
}

func TestTonapiThenCoinGeckoThenStale(t *testing.T) {
	var tonapiUp, geckoUp atomic.Bool
	tonapiUp.Store(true)
	geckoUp.Store(true)
	tonapi := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if !tonapiUp.Load() {
			writer.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = writer.Write([]byte(`{"rates":{"TON":{"prices":{"RUB":251.237}}}}`))
	}))
	defer tonapi.Close()
	gecko := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if !geckoUp.Load() {
			writer.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = writer.Write([]byte(`{"the-open-network":{"rub":240.5}}`))
	}))
	defer gecko.Close()

	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	service := New(Options{Currency: "RUB", Exponent: 2, TonapiURL: tonapi.URL, CoinGeckoURL: gecko.URL,
		Clock: func() time.Time { return now }})

	rate, err := service.Current(context.Background())
	if err != nil || rate.MinorPerTON != 25_124 || rate.Source != SourceTonapi {
		t.Fatalf("tonapi rate = %+v, %v", rate, err)
	}

	tonapiUp.Store(false)
	now = now.Add(6 * time.Minute)
	rate, err = service.Current(context.Background())
	if err != nil || rate.MinorPerTON != 24_050 || rate.Source != SourceCoinGecko {
		t.Fatalf("fallback rate = %+v, %v", rate, err)
	}

	geckoUp.Store(false)
	now = now.Add(30 * time.Minute)
	rate, err = service.Current(context.Background())
	if err != nil || rate.Source != SourceCoinGecko {
		t.Fatalf("a stale rate within the hour must still be served: %+v, %v", rate, err)
	}

	now = now.Add(2 * time.Hour)
	if _, err = service.Current(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("past the hour the rate must be unknown, got %v", err)
	}
}

func TestMinorFromNano(t *testing.T) {
	rate := Rate{MinorPerTON: 25_000}
	if got := rate.MinorFromNano(1_500_000_000); got != 37_500 {
		t.Fatalf("1.5 TON at 250 RUB = %d kopeks", got)
	}
	if got := rate.MinorFromNano(1); got != 0 {
		t.Fatalf("rounding must be down, got %d", got)
	}
}
