//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/adtracking"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// Advertising measurement against a real database.
//
// The property that could not be built before is the one worth asserting: a
// click identifier captured on a visit reaching the export of an order that
// settled a day later. Everything between those two points is SQL — a table
// constraint, a join on `paid_at`, and a summary — and none of it can be
// checked in Go.

func TestAClickReachesTheExportOfAnOrderThatSettlesLater(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)

	customer := seedCustomer(ctx, t, harness, "clicked@example.test")

	// The click happened yesterday; the money arrived today. That gap is the
	// whole reason a counter script cannot answer this.
	settled := time.Now().UTC().Add(-2 * time.Hour)
	order := seedAttributedOrder(ctx, t, harness, customer, "purchase", "USD", 2500, settled,
		adtracking.Attribution{
			ClickID: "Cj0KCQiA_ABC-123", ClickSource: "google",
			Source: "google", Medium: "cpc", Campaign: "spring",
		})

	// An order from an ordinary visit has no attribution and is absent from
	// both reports, because there is no advertisement to credit.
	seedSettledOrder(ctx, t, harness, customer, "purchase", "USD", 1500, settled)

	from, to := settled.Add(-24*time.Hour), time.Now().UTC().Add(time.Hour)
	conversions, err := service.Conversions(ctx, from, to, "")
	if err != nil {
		t.Fatalf("conversions: %v", err)
	}
	if len(conversions) != 1 {
		t.Fatalf("%d conversions, want only the attributed one", len(conversions))
	}
	if conversions[0].OrderID != order || conversions[0].ClickID != "Cj0KCQiA_ABC-123" {
		t.Fatalf("the conversion reads %+v", conversions[0])
	}
	if conversions[0].PaidMinor != 2500 || conversions[0].Currency != "USD" {
		t.Fatalf("the amount reads %+v", conversions[0])
	}

	// Filtering by platform is what makes a per-network upload possible.
	if got, err := service.Conversions(ctx, from, to, "yandex"); err != nil || len(got) != 0 {
		t.Fatalf("filtering to another platform returned %d rows (%v)", len(got), err)
	}

	channels, err := service.Channels(ctx, from, to)
	if err != nil {
		t.Fatalf("channels: %v", err)
	}
	if len(channels) != 1 {
		t.Fatalf("%d channels, want one: %+v", len(channels), channels)
	}
	if channels[0].Channel != "google" || channels[0].Orders != 1 {
		t.Fatalf("the channel reads %+v", channels[0])
	}
	// Uploadable is the count that matters: an order can be attributed to a
	// channel by a UTM tag and still carry no click identifier to match on.
	if channels[0].AttributedClicks != 1 {
		t.Fatalf("the uploadable count reads %d", channels[0].AttributedClicks)
	}
}

// An unsettled order is not a conversion. The export is keyed on when the money
// arrived, so a draft nobody paid for cannot teach a platform anything.
func TestAnUnpaidOrderIsNotAConversion(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)

	customer := seedCustomer(ctx, t, harness, "abandoned@example.test")
	if _, err := harness.pool.Exec(ctx, `
		WITH created AS (
			INSERT INTO orders (user_id, state, operation, currency, subtotal_minor,
				external_minor, idempotency_key)
			VALUES ($1::uuid, 'pending', 'purchase', 'USD', 2500, 2500, $2) RETURNING id
		)
		INSERT INTO order_attributions (order_id, click_id, click_source)
		SELECT id, 'Cj0KCQiA_XYZ-789', 'google' FROM created`,
		customer, "abandoned-"+customer,
	); err != nil {
		t.Fatalf("seed abandoned order: %v", err)
	}

	conversions, err := service.Conversions(
		ctx, time.Now().UTC().Add(-24*time.Hour), time.Now().UTC().Add(time.Hour), "")
	if err != nil {
		t.Fatalf("conversions: %v", err)
	}
	if len(conversions) != 0 {
		t.Fatalf("an unpaid order was exported as a conversion: %+v", conversions)
	}
}

// The counter identifier is interpolated into a script that runs in every
// visitor's browser. The refusal has to happen before the row is written.
func TestACounterThatCouldCarryCodeIsNeverStored(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "analytics@example.test")

	settings, err := service.Analytics(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// A fresh installation measures nothing and has nothing configured, which is
	// what makes "off by default" true rather than aspirational.
	if settings.Enabled || len(settings.Counters) != 0 {
		t.Fatalf("a fresh installation reads %+v", settings.Settings)
	}

	_, err = service.SaveAnalytics(ctx, adtracking.Settings{
		Enabled:  true,
		Counters: map[adtracking.Provider]string{adtracking.ProviderMetrica: `1);alert(1)//`},
	}, settings.Version, actor)
	if !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("an identifier carrying code was accepted: %v", err)
	}

	// The section's row exists from the migration, like every other one. What
	// must not have happened is the document changing.
	var document string
	if err := harness.pool.QueryRow(ctx,
		`SELECT document::text FROM installation_settings WHERE section = 'analytics'`,
	).Scan(&document); err != nil {
		t.Fatalf("read document: %v", err)
	}
	if document != "{}" {
		t.Fatalf("a refused configuration was stored anyway: %s", document)
	}

	// And the valid one saves, renders, and is what the storefront reads.
	saved, err := service.SaveAnalytics(ctx, adtracking.Settings{
		Enabled:  true,
		Counters: map[adtracking.Provider]string{adtracking.ProviderMetrica: "12345678"},
		Verifications: []adtracking.Verification{
			{Name: "yandex-verification", Content: "a1b2c3d4e5f6"},
		},
	}, settings.Version, actor)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.Counters[adtracking.ProviderMetrica] != "12345678" {
		t.Fatalf("the saved counter reads %+v", saved.Counters)
	}

	public, err := service.PublicAnalytics(ctx)
	if err != nil {
		t.Fatalf("public: %v", err)
	}
	if !public.Measurable || public.Counters["yandex_metrica"] != "12345678" {
		t.Fatalf("the storefront reads %+v", public)
	}
	if len(public.Verifications) != 1 {
		t.Fatalf("the verification tag did not reach the storefront: %+v", public)
	}
}

// Turning measurement off keeps the identifiers but publishes none of them, so
// an operator switching it off in a hurry does not have to find the numbers
// again to switch it back on.
func TestTurningMeasurementOffPublishesNoCounter(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	actor := harness.operator(ctx, t, "analytics-off@example.test")

	settings, _ := service.Analytics(ctx)
	saved, err := service.SaveAnalytics(ctx, adtracking.Settings{
		Enabled:  false,
		Counters: map[adtracking.Provider]string{adtracking.ProviderGA4: "G-AB12CD34"},
		Verifications: []adtracking.Verification{
			{Name: "google-site-verification", Content: "token-value-1234"},
		},
	}, settings.Version, actor)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.Counters[adtracking.ProviderGA4] != "G-AB12CD34" {
		t.Fatal("the identifier was discarded when measurement was switched off")
	}

	public, err := service.PublicAnalytics(ctx)
	if err != nil {
		t.Fatalf("public: %v", err)
	}
	if public.Measurable || len(public.Counters) != 0 {
		t.Fatalf("a counter is published with measurement off: %+v", public)
	}
	// The verification tag still is. It observes nobody, and a tag that only
	// appeared when measurement was on would stop verifying the moment somebody
	// turned it off.
	if len(public.Verifications) != 1 {
		t.Fatalf("the verification tag was withheld: %+v", public)
	}
}

func seedAttributedOrder(
	ctx context.Context, t *testing.T, harness *harness,
	customerID, operation, currency string, amount int64, paidAt time.Time,
	attribution adtracking.Attribution,
) string {
	t.Helper()
	var orderID string
	if err := harness.pool.QueryRow(ctx, `
		INSERT INTO orders (user_id, state, operation, currency, subtotal_minor,
			external_minor, paid_minor, idempotency_key, paid_at)
		VALUES ($1::uuid, 'paid', $2, $3, $4, $4, $4, $5, $6) RETURNING id::text`,
		customerID, operation, currency, amount,
		"attributed-"+customerID+"-"+paidAt.String(), paidAt,
	).Scan(&orderID); err != nil {
		t.Fatalf("seed attributed order: %v", err)
	}
	if _, err := harness.pool.Exec(ctx, `
		INSERT INTO order_attributions (order_id, click_id, click_source, utm_source,
			utm_medium, utm_campaign)
		VALUES ($1::uuid, NULLIF($2, ''), NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, ''))`,
		orderID, attribution.ClickID, attribution.ClickSource, attribution.Source,
		attribution.Medium, attribution.Campaign,
	); err != nil {
		t.Fatalf("seed attribution: %v", err)
	}
	return orderID
}
