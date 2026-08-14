package panelpg

import (
	"context"
	"fmt"
	"time"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Payment health per provider.
//
// With four bundled adapters, an acquirer that starts refusing cards is visible
// today as support tickets and a growing stuck-payment queue rather than as a
// number that moved. This is the number.
//
// It reports two rates rather than one, and the distinction is the substance:
// a customer who abandons a checkout and a gateway that refuses a card are not
// the same event, and only the second is the provider's fault. Collapsing them
// produces a figure that drops every time marketing sends a campaign to people
// who were never going to buy.

// ProviderHealthLine is one adapter's outcomes in one currency.
type ProviderHealthLine struct {
	Provider string `json:"provider"`
	Currency string `json:"currency"`
	Intents  int64  `json:"intents"`
	Settled  int64  `json:"settled"`
	// Failed is the provider refusing or erroring.
	Failed int64 `json:"failed"`
	// Abandoned is the customer walking away — cancelled or expired. It is the
	// customer's decision, not the acquirer's performance.
	Abandoned int64 `json:"abandoned"`
	// StillOpen is in neither rate's denominator. An intent created five minutes
	// ago has not failed, and counting it as one would make the current hour
	// look catastrophic exactly when somebody is watching.
	StillOpen    int64 `json:"stillOpen"`
	SettledMinor int64 `json:"settledMinor"`

	MedianSettleSeconds int64 `json:"medianSettleSeconds"`
	P95SettleSeconds    int64 `json:"p95SettleSeconds"`
	// OldestOpenSeconds is the stuck-payment queue as a number.
	OldestOpenSeconds int64 `json:"oldestOpenSeconds"`

	// SettlementRate is settled / (settled + failed): how often the provider
	// completes a payment it was asked to take. Nil when nothing reached a
	// decision, because zero would read as total failure.
	SettlementRate *float64 `json:"settlementRate,omitempty"`
	// CompletionRate is settled / (settled + failed + abandoned): the funnel,
	// including customers who changed their mind.
	CompletionRate *float64 `json:"completionRate,omitempty"`
}

// ProviderHealthDay is one adapter on one day.
type ProviderHealthDay struct {
	Day      string `json:"day"`
	Provider string `json:"provider"`
	Intents  int64  `json:"intents"`
	Settled  int64  `json:"settled"`
	Failed   int64  `json:"failed"`
}

// WebhookHealthLine is one adapter's notification intake.
type WebhookHealthLine struct {
	Provider string `json:"provider"`
	Received int64  `json:"received"`
	// Rejected failed signature verification. A non-zero count is either a
	// rotated secret or somebody probing the endpoint, and both are worth
	// seeing.
	Rejected  int64 `json:"rejected"`
	Failed    int64 `json:"failed"`
	Processed int64 `json:"processed"`
}

// PaymentHealthReport is the whole screen in one read.
type PaymentHealthReport struct {
	Since       time.Time            `json:"since"`
	Until       time.Time            `json:"until"`
	Timezone    string               `json:"timezone"`
	Providers   []ProviderHealthLine `json:"providers"`
	ByDay       []ProviderHealthDay  `json:"byDay"`
	Webhooks    []WebhookHealthLine  `json:"webhooks"`
	GeneratedAt time.Time            `json:"generatedAt"`
}

// PaymentHealth reads settlement, latency, and webhook intake per adapter.
func (service *Service) PaymentHealth(
	ctx context.Context, since, until time.Time, timezone string,
) (PaymentHealthReport, error) {
	if !until.After(since) {
		return PaymentHealthReport{}, wrapValidation(
			fmt.Errorf("the period ends before it starts"))
	}
	if until.Sub(since) > MaxSalesReportWindow {
		return PaymentHealthReport{}, wrapValidation(fmt.Errorf(
			"a report covers at most %d days", int(MaxSalesReportWindow.Hours()/24),
		))
	}
	if timezone == "" {
		timezone = "UTC"
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return PaymentHealthReport{}, wrapValidation(
			fmt.Errorf("%q is not a known timezone", timezone))
	}

	queries := service.queries()
	report := PaymentHealthReport{
		Since: since.UTC(), Until: until.UTC(), Timezone: timezone,
		Providers: []ProviderHealthLine{}, ByDay: []ProviderHealthDay{},
		Webhooks: []WebhookHealthLine{}, GeneratedAt: time.Now().UTC(),
	}

	providers, err := queries.PaymentHealthByProvider(ctx, dbgen.PaymentHealthByProviderParams{
		Since: timestamp(since), Until: timestamp(until),
	})
	if err != nil {
		return PaymentHealthReport{}, err
	}
	for _, row := range providers {
		line := ProviderHealthLine{
			Provider: row.Provider, Currency: row.Currency, Intents: row.Intents,
			Settled: row.Settled, Failed: row.Failed, Abandoned: row.Abandoned,
			StillOpen: row.StillOpen, SettledMinor: row.SettledMinor,
			MedianSettleSeconds: row.MedianSettleSeconds,
			P95SettleSeconds:    row.P95SettleSeconds,
			OldestOpenSeconds:   row.OldestOpenSeconds,
		}
		line.SettlementRate = rate(row.Settled, row.Settled+row.Failed)
		line.CompletionRate = rate(row.Settled, row.Settled+row.Failed+row.Abandoned)
		report.Providers = append(report.Providers, line)
	}

	days, err := queries.PaymentHealthByDay(ctx, dbgen.PaymentHealthByDayParams{
		Since: timestamp(since), Until: timestamp(until), Timezone: timezone,
	})
	if err != nil {
		return PaymentHealthReport{}, err
	}
	for _, row := range days {
		report.ByDay = append(report.ByDay, ProviderHealthDay{
			Day: row.Day.Time.Format(time.DateOnly), Provider: row.Provider,
			Intents: row.Intents, Settled: row.Settled, Failed: row.Failed,
		})
	}

	webhooks, err := queries.WebhookHealthByProvider(ctx, dbgen.WebhookHealthByProviderParams{
		Since: timestamp(since), Until: timestamp(until),
	})
	if err != nil {
		return PaymentHealthReport{}, err
	}
	for _, row := range webhooks {
		report.Webhooks = append(report.Webhooks, WebhookHealthLine{
			Provider: row.Provider, Received: row.Received, Rejected: row.Rejected,
			Failed: row.Failed, Processed: row.Processed,
		})
	}
	return report, nil
}

// rate returns a proportion, or nil when the denominator is zero.
//
// Nil rather than zero because the two mean opposite things: no payment reached
// a decision, against every payment failing. A screen that rendered 0% for a
// quiet provider would send somebody to investigate an adapter nobody used.
func rate(numerator, denominator int64) *float64 {
	if denominator <= 0 {
		return nil
	}
	value := float64(numerator) / float64(denominator)
	return &value
}
