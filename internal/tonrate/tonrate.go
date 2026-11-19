// Package tonrate prices TON in the shop's currency, so the cost of a Stars
// delivery paid in TON can be fixed in the currency the customer paid.
//
// The rate is read from tonapi.io (no key needed) with CoinGecko as a
// fallback, or taken from the operator's override. A successful read is
// cached for five minutes; when both sources fail, the last good rate is used
// for up to an hour and then the rate is reported as unknown rather than
// guessed. That mirrors the fork's ton_rate_service exactly.
package tonrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Sources a rate can come from. They match the values stored with a delivery.
const (
	SourceManual    = "manual"
	SourceTonapi    = "tonapi"
	SourceCoinGecko = "coingecko"
)

const (
	cacheTTL = 5 * time.Minute
	staleTTL = time.Hour
	nanoTON  = 1_000_000_000
)

// ErrUnavailable reports that no rate could be obtained from anywhere.
var ErrUnavailable = errors.New("TON exchange rate is unavailable")

// Rate is the price of one TON in minor units of a currency.
type Rate struct {
	MinorPerTON int64
	Currency    string
	Source      string
}

// MinorFromNano converts a nanoton amount at this rate, rounding down as the
// fork does. The arithmetic is exact for any amount a wallet can hold.
func (rate Rate) MinorFromNano(nano int64) int64 {
	if nano <= 0 || rate.MinorPerTON <= 0 {
		return 0
	}
	product := new(big.Int).Mul(big.NewInt(nano), big.NewInt(rate.MinorPerTON))
	return product.Quo(product, big.NewInt(nanoTON)).Int64()
}

// Options configures a Service.
type Options struct {
	// Currency is the shop currency, an ISO 4217 code.
	Currency string
	// Exponent is how many minor units make a major one (2 for RUB).
	Exponent int
	// ManualMinorPerTON, when positive, is used instead of any network source.
	ManualMinorPerTON int64
	HTTP              *http.Client
	// TonapiURL and CoinGeckoURL override the sources, for tests.
	TonapiURL    string
	CoinGeckoURL string
	Clock        func() time.Time
}

// Service reads and caches the rate.
type Service struct {
	options Options
	mu      sync.Mutex
	cached  Rate
	at      time.Time
}

// New builds the rate service.
func New(options Options) *Service {
	options.Currency = strings.ToUpper(strings.TrimSpace(options.Currency))
	if options.Currency == "" {
		options.Currency = "RUB"
	}
	if options.HTTP == nil {
		options.HTTP = &http.Client{Timeout: 5 * time.Second}
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	lower := strings.ToLower(options.Currency)
	if options.TonapiURL == "" {
		options.TonapiURL = "https://tonapi.io/v2/rates?tokens=ton&currencies=" + lower
	}
	if options.CoinGeckoURL == "" {
		options.CoinGeckoURL = "https://api.coingecko.com/api/v3/simple/price?ids=the-open-network&vs_currencies=" + lower
	}
	return &Service{options: options}
}

// Current returns the rate, from the override, the cache, or the network.
func (service *Service) Current(ctx context.Context) (Rate, error) {
	if service.options.ManualMinorPerTON > 0 {
		return Rate{
			MinorPerTON: service.options.ManualMinorPerTON,
			Currency:    service.options.Currency, Source: SourceManual,
		}, nil
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	now := service.options.Clock()
	if service.cached.MinorPerTON > 0 && now.Sub(service.at) < cacheTTL {
		return service.cached, nil
	}
	for _, source := range []struct {
		name string
		read func(context.Context) (float64, error)
	}{
		{SourceTonapi, service.readTonapi},
		{SourceCoinGecko, service.readCoinGecko},
	} {
		value, err := source.read(ctx)
		if err != nil {
			continue
		}
		minor := toMinor(value, service.options.Exponent)
		if minor <= 0 {
			continue
		}
		service.cached = Rate{MinorPerTON: minor, Currency: service.options.Currency, Source: source.name}
		service.at = now
		return service.cached, nil
	}
	if service.cached.MinorPerTON > 0 && now.Sub(service.at) < staleTTL {
		return service.cached, nil
	}
	return Rate{}, ErrUnavailable
}

func (service *Service) readTonapi(ctx context.Context) (float64, error) {
	var response struct {
		Rates map[string]struct {
			Prices map[string]float64 `json:"prices"`
		} `json:"rates"`
	}
	if err := service.get(ctx, service.options.TonapiURL, &response); err != nil {
		return 0, err
	}
	for token, rate := range response.Rates {
		if !strings.EqualFold(token, "TON") {
			continue
		}
		for currency, price := range rate.Prices {
			if strings.EqualFold(currency, service.options.Currency) {
				return price, nil
			}
		}
	}
	return 0, ErrUnavailable
}

func (service *Service) readCoinGecko(ctx context.Context) (float64, error) {
	var response map[string]map[string]float64
	if err := service.get(ctx, service.options.CoinGeckoURL, &response); err != nil {
		return 0, err
	}
	for currency, price := range response["the-open-network"] {
		if strings.EqualFold(currency, service.options.Currency) {
			return price, nil
		}
	}
	return 0, ErrUnavailable
}

func (service *Service) get(ctx context.Context, target string, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := service.options.HTTP.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
		return fmt.Errorf("rate source answered HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(out)
}

func toMinor(value float64, exponent int) int64 {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return int64(math.Round(value * math.Pow10(exponent)))
}
