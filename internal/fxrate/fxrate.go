// Package fxrate holds the two rates multi-currency selling needs: the price
// of one Telegram Star in the shop currency, so Stars can pay a ruble-priced
// order, and the USD/RUB rate that the automatic Star price is derived from.
//
// USD/RUB is read from the Central Bank of Russia's daily rate with
// open.er-api.com as the fallback, cached for an hour, and persisted, so a
// restart during an outage still has the last good reading. A reading older
// than two days is not used; the operator's manual value is then the last
// resort, and with none the rate is reported unavailable rather than guessed.
package fxrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// Sources a USD/RUB reading can come from.
const (
	SourceCBR      = "cbr"
	SourceOpenER   = "open_er_api"
	SourceManual   = "manual"
	SourceStored   = "stored"
	cacheTTL       = time.Hour
	staleTTL       = 48 * time.Hour
	settingsTTL    = 30 * time.Second
	usdRubPair     = "USD/RUB"
	defaultCBRURL  = "https://www.cbr-xml-daily.ru/daily_json.js"
	defaultOpenURL = "https://open.er-api.com/v6/latest/USD"
)

// Errors callers act on.
var (
	ErrUnavailable = errors.New("the exchange rate is unavailable")
	ErrInvalid     = errors.New("the currency settings are not valid")
	ErrNotQuotable = errors.New("this order cannot be paid in Telegram Stars")
)

// Settings is the operator's configuration.
type Settings struct {
	StarsEnabled      bool   `json:"starsEnabled"`
	StarsCurrency     string `json:"starsCurrency"`
	StarsRateMode     string `json:"starsRateMode"`
	StarsRateMinor    int64  `json:"starsRateMinor"`
	StarsUSDMicros    int64  `json:"starsUsdMicros"`
	USDRUBMode        string `json:"usdRubMode"`
	USDRUBManualMinor *int64 `json:"usdRubManualMinor,omitempty"`
}

// Rate is one reading: minor units of the quote currency per unit.
type Rate struct {
	Minor      int64     `json:"minor"`
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observedAt"`
}

// Options configures the service.
type Options struct {
	HTTP    *http.Client
	CBRURL  string
	OpenURL string
	Clock   func() time.Time
}

// Service reads and caches the rates.
type Service struct {
	pool    *pgxpool.Pool
	options Options

	mu         sync.Mutex
	usd        Rate
	settings   Settings
	settingsAt time.Time
}

// New builds the service.
func New(pool *pgxpool.Pool, options Options) *Service {
	if options.HTTP == nil {
		options.HTTP = &http.Client{Timeout: 5 * time.Second}
	}
	if options.CBRURL == "" {
		options.CBRURL = defaultCBRURL
	}
	if options.OpenURL == "" {
		options.OpenURL = defaultOpenURL
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	return &Service{pool: pool, options: options}
}

// Settings reads the configuration.
func (service *Service) Settings(ctx context.Context) (Settings, error) {
	var settings Settings
	err := service.pool.QueryRow(ctx, `SELECT stars_enabled, stars_currency, stars_rate_mode, stars_rate_minor,
			stars_usd_micros, usd_rub_mode, usd_rub_manual_minor FROM currency_settings`).Scan(
		&settings.StarsEnabled, &settings.StarsCurrency, &settings.StarsRateMode, &settings.StarsRateMinor,
		&settings.StarsUSDMicros, &settings.USDRUBMode, &settings.USDRUBManualMinor)
	return settings, err
}

func (service *Service) cachedSettings(ctx context.Context) (Settings, error) {
	service.mu.Lock()
	if service.options.Clock().Sub(service.settingsAt) < settingsTTL {
		settings := service.settings
		service.mu.Unlock()
		return settings, nil
	}
	service.mu.Unlock()
	settings, err := service.Settings(ctx)
	if err != nil {
		return Settings{}, err
	}
	service.mu.Lock()
	service.settings, service.settingsAt = settings, service.options.Clock()
	service.mu.Unlock()
	return settings, nil
}

// SaveSettings validates and stores the configuration.
func (service *Service) SaveSettings(ctx context.Context, settings Settings, actor panelpg.Actor) (Settings, error) {
	settings.StarsCurrency = strings.ToUpper(strings.TrimSpace(settings.StarsCurrency))
	if settings.StarsCurrency == "" {
		settings.StarsCurrency = "RUB"
	}
	valid := len(settings.StarsCurrency) == 3 && settings.StarsCurrency != "XTR" &&
		(settings.StarsRateMode == "manual" || settings.StarsRateMode == "auto") &&
		settings.StarsRateMinor > 0 && settings.StarsUSDMicros > 0 &&
		(settings.USDRUBMode == "auto" || settings.USDRUBMode == "manual" && settings.USDRUBManualMinor != nil) &&
		(settings.USDRUBManualMinor == nil || *settings.USDRUBManualMinor > 0) &&
		// The automatic Star price is derived from USD/RUB, which prices in rubles.
		(settings.StarsRateMode == "manual" || settings.StarsCurrency == "RUB")
	if !valid {
		return Settings{}, ErrInvalid
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return Settings{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `UPDATE currency_settings SET stars_enabled = $1, stars_currency = $2, stars_rate_mode = $3,
			stars_rate_minor = $4, stars_usd_micros = $5, usd_rub_mode = $6, usd_rub_manual_minor = $7,
			updated_at = now(), updated_by = NULLIF($8, '')::uuid`,
		settings.StarsEnabled, settings.StarsCurrency, settings.StarsRateMode, settings.StarsRateMinor,
		settings.StarsUSDMicros, settings.USDRUBMode, settings.USDRUBManualMinor, actor.AdminID); err != nil {
		return Settings{}, err
	}
	if err = panelpg.AppendAudit(ctx, tx, actor, "panel.currency.settings_saved", "configuration", "currency", "settings",
		map[string]any{"starsEnabled": settings.StarsEnabled, "starsRateMode": settings.StarsRateMode,
			"starsRateMinor": settings.StarsRateMinor, "usdRubMode": settings.USDRUBMode}); err != nil {
		return Settings{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Settings{}, err
	}
	service.mu.Lock()
	service.settingsAt = time.Time{}
	service.mu.Unlock()
	return settings, nil
}

// USDRUB returns the current USD/RUB rate in kopecks per dollar.
func (service *Service) USDRUB(ctx context.Context) (Rate, error) {
	settings, err := service.cachedSettings(ctx)
	if err != nil {
		return Rate{}, err
	}
	now := service.options.Clock()
	if settings.USDRUBMode == "manual" && settings.USDRUBManualMinor != nil {
		return Rate{Minor: *settings.USDRUBManualMinor, Source: SourceManual, ObservedAt: now}, nil
	}
	service.mu.Lock()
	cached := service.usd
	service.mu.Unlock()
	if cached.Minor > 0 && now.Sub(cached.ObservedAt) < cacheTTL {
		return cached, nil
	}
	for _, source := range []struct {
		name string
		read func(context.Context) (float64, error)
	}{{SourceCBR, service.readCBR}, {SourceOpenER, service.readOpenER}} {
		value, readErr := source.read(ctx)
		minor := int64(math.Round(value * 100))
		if readErr != nil || minor <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		rate := Rate{Minor: minor, Source: source.name, ObservedAt: now}
		service.mu.Lock()
		service.usd = rate
		service.mu.Unlock()
		_, _ = service.pool.Exec(ctx, `INSERT INTO exchange_rate_observations (pair, minor, source, observed_at)
			VALUES ($1, $2, $3, $4) ON CONFLICT (pair) DO UPDATE SET minor = $2, source = $3, observed_at = $4`,
			usdRubPair, rate.Minor, rate.Source, rate.ObservedAt)
		return rate, nil
	}
	// Every source failed: the last good reading, from memory or from the
	// database, while it is less than two days old.
	if cached.Minor <= 0 {
		var stored Rate
		if err := service.pool.QueryRow(ctx, `SELECT minor, observed_at FROM exchange_rate_observations WHERE pair = $1`,
			usdRubPair).Scan(&stored.Minor, &stored.ObservedAt); err == nil {
			stored.Source = SourceStored
			cached = stored
		}
	}
	if cached.Minor > 0 && now.Sub(cached.ObservedAt) < staleTTL {
		return cached, nil
	}
	if settings.USDRUBManualMinor != nil {
		return Rate{Minor: *settings.USDRUBManualMinor, Source: SourceManual, ObservedAt: now}, nil
	}
	return Rate{}, ErrUnavailable
}

// StarsRate returns the price of one Star in minor units of the configured
// currency, and whether Stars may pay orders in that currency at all.
func (service *Service) StarsRate(ctx context.Context) (string, int64, bool, error) {
	settings, err := service.cachedSettings(ctx)
	if err != nil || !settings.StarsEnabled {
		return "", 0, false, err
	}
	if settings.StarsRateMode == "manual" {
		return settings.StarsCurrency, settings.StarsRateMinor, true, nil
	}
	usd, err := service.USDRUB(ctx)
	if err != nil {
		return "", 0, false, err
	}
	// kopecks per Star = kopecks per dollar × dollars per Star.
	minor := int64(math.Ceil(float64(usd.Minor) * float64(settings.StarsUSDMicros) / 1_000_000))
	return settings.StarsCurrency, max(minor, 1), true, nil
}

// Convertible reports whether Stars may pay an order in the currency. It is
// what the Telegram Stars adapter asks before it is offered for a ruble order.
func (service *Service) Convertible(currency string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	starsCurrency, _, ok, err := service.StarsRate(ctx)
	return err == nil && ok && strings.EqualFold(currency, starsCurrency)
}

// StarsFor is the Star count an amount costs: rounded up, so a payment never
// covers less than the order.
func StarsFor(amountMinor, rateMinor int64) int64 {
	if amountMinor <= 0 || rateMinor <= 0 {
		return 0
	}
	return (amountMinor + rateMinor - 1) / rateMinor
}

// Quote is the fixed Stars price of one order.
type Quote struct {
	AmountMinor int64
	Currency    string
	Stars       int64
	RateMinor   int64
}

// QuoteOrder fixes the Stars price of a pending order the first time it is
// asked for and returns the same price afterwards, so the invoice, Telegram's
// pre-checkout probe, and settlement agree.
func (service *Service) QuoteOrder(ctx context.Context, orderID string) (Quote, error) {
	var quote Quote
	err := service.pool.QueryRow(ctx, `SELECT q.amount_minor, q.currency, q.stars, q.rate_minor
		FROM stars_quotes q JOIN orders o ON o.id = q.order_id
		WHERE q.order_id = $1::uuid AND q.amount_minor = o.external_minor AND q.currency = o.currency`, orderID).
		Scan(&quote.AmountMinor, &quote.Currency, &quote.Stars, &quote.RateMinor)
	if err == nil {
		return quote, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Quote{}, err
	}
	var state string
	if err = service.pool.QueryRow(ctx, `SELECT state, external_minor, currency FROM orders WHERE id = $1::uuid`, orderID).
		Scan(&state, &quote.AmountMinor, &quote.Currency); err != nil {
		return Quote{}, err
	}
	currency, rate, ok, err := service.StarsRate(ctx)
	if err != nil {
		return Quote{}, err
	}
	if !ok || currency != quote.Currency || quote.AmountMinor <= 0 || (state != "pending" && state != "draft") {
		return Quote{}, ErrNotQuotable
	}
	quote.RateMinor, quote.Stars = rate, StarsFor(quote.AmountMinor, rate)
	if _, err = service.pool.Exec(ctx, `INSERT INTO stars_quotes (order_id, amount_minor, currency, stars, rate_minor)
		VALUES ($1::uuid, $2, $3, $4, $5)
		ON CONFLICT (order_id) DO UPDATE SET amount_minor = $2, currency = $3, stars = $4, rate_minor = $5, created_at = now()`,
		orderID, quote.AmountMinor, quote.Currency, quote.Stars, quote.RateMinor); err != nil {
		return Quote{}, err
	}
	return quote, nil
}

func (service *Service) readCBR(ctx context.Context) (float64, error) {
	var answer struct {
		Valute map[string]struct {
			Nominal float64 `json:"Nominal"`
			Value   float64 `json:"Value"`
		} `json:"Valute"`
	}
	if err := service.get(ctx, service.options.CBRURL, &answer); err != nil {
		return 0, err
	}
	usd, ok := answer.Valute["USD"]
	if !ok || usd.Value <= 0 {
		return 0, ErrUnavailable
	}
	if usd.Nominal > 0 {
		return usd.Value / usd.Nominal, nil
	}
	return usd.Value, nil
}

func (service *Service) readOpenER(ctx context.Context) (float64, error) {
	var answer struct {
		Result string             `json:"result"`
		Rates  map[string]float64 `json:"rates"`
	}
	if err := service.get(ctx, service.options.OpenURL, &answer); err != nil {
		return 0, err
	}
	if rub := answer.Rates["RUB"]; rub > 0 {
		return rub, nil
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
