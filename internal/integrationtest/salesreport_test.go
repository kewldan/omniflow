//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/panelpg"
)

// Sales reporting against a real database.
//
// The property worth a container is the one the `paid_at` column exists for: a
// report over a closed period must return the same figures however much happens
// to those orders afterwards. Nothing that runs entirely in Go can prove that,
// because the thing being proved is which timestamp the statements write.

func TestAClosedPeriodDoesNotMoveWhenARefundIsRecorded(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)

	// One settled order, dated inside the window.
	customer := seedCustomer(ctx, t, harness, "sales-report@example.test")
	paidAt := time.Now().UTC().Add(-48 * time.Hour)
	seedSettledOrder(ctx, t, harness, customer, "purchase", "USD", 5000, paidAt)

	since, until := paidAt.Add(-time.Hour), paidAt.Add(time.Hour)
	before, err := service.SalesReport(ctx, since, until, "UTC", "")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(before.ByOperation) != 1 || before.ByOperation[0].PaidMinor != 5000 {
		t.Fatalf("the sale is not in the period: %+v", before.ByOperation)
	}

	// Now touch every row the way a later refund would: `updated_at` moves,
	// `paid_at` must not. Against the previous implementation, which keyed on
	// `updated_at`, this is exactly the write that emptied a closed month.
	if _, err := harness.pool.Exec(ctx,
		`UPDATE orders SET state = 'partially_refunded', refunded_minor = 1000, updated_at = now()`,
	); err != nil {
		t.Fatalf("refund: %v", err)
	}

	after, err := service.SalesReport(ctx, since, until, "UTC", "")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(after.ByOperation) != 1 || after.ByOperation[0].PaidMinor != 5000 {
		t.Fatalf(
			"a refund recorded today changed a closed period: %+v", after.ByOperation,
		)
	}
}

func TestTheReportSeparatesKindsOfSaleAndCurrencies(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)

	customer := seedCustomer(ctx, t, harness, "sales-kinds@example.test")
	paidAt := time.Now().UTC().Add(-24 * time.Hour)
	seedSettledOrder(ctx, t, harness, customer, "purchase", "USD", 5000, paidAt)
	seedSettledOrder(ctx, t, harness, customer, "renewal", "USD", 4500, paidAt)
	seedSettledOrder(ctx, t, harness, customer, "topup", "EUR", 2000, paidAt)
	// A draft is not a sale. It has no paid_at, so it cannot appear.
	seedDraftOrder(ctx, t, harness, customer, "USD", 9999)

	report, err := service.SalesReport(
		ctx, paidAt.Add(-time.Hour), paidAt.Add(time.Hour), "UTC", "")
	if err != nil {
		t.Fatalf("report: %v", err)
	}

	found := map[string]int64{}
	for _, line := range report.ByOperation {
		found[line.Operation+"/"+line.Currency] = line.PaidMinor
	}
	for key, want := range map[string]int64{
		"purchase/USD": 5000, "renewal/USD": 4500, "topup/EUR": 2000,
	} {
		if found[key] != want {
			t.Errorf("%s is %d, want %d — the breakdown is what separates new "+
				"business from renewals and from money that only moved into a wallet",
				key, found[key], want)
		}
	}
	if len(report.ByOperation) != 3 {
		t.Fatalf("%d lines, want 3; an unsettled order reached a revenue report: %+v",
			len(report.ByOperation), report.ByOperation)
	}

	// Filtering restricts every section rather than only the one it names.
	filtered, err := service.SalesReport(
		ctx, paidAt.Add(-time.Hour), paidAt.Add(time.Hour), "UTC", "EUR")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	for _, line := range filtered.ByOperation {
		if line.Currency != "EUR" {
			t.Errorf("a currency filter let %s through", line.Currency)
		}
	}
	for _, day := range filtered.ByDay {
		if day.Currency != "EUR" {
			t.Errorf("the daily section ignored the currency filter")
		}
	}
}

func TestTheReportRefusesAPeriodItCannotServe(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)
	now := time.Now().UTC()

	for name, period := range map[string][2]time.Time{
		"backwards": {now, now.Add(-time.Hour)},
		"too long":  {now.Add(-5 * 365 * 24 * time.Hour), now},
	} {
		if _, err := service.SalesReport(ctx, period[0], period[1], "UTC", ""); !errors.Is(
			err, panelpg.ErrValidaton,
		) {
			t.Errorf("a %s period was accepted: %v", name, err)
		}
	}

	// A timezone the database does not know would otherwise fail deep inside a
	// query, which is a 500 for what is a typed field.
	if _, err := service.SalesReport(
		ctx, now.Add(-time.Hour), now, "Mars/Olympus", "",
	); !errors.Is(err, panelpg.ErrValidaton) {
		t.Errorf("an unknown timezone was accepted: %v", err)
	}
}

// The day bucket follows the operator's timezone, not UTC. A sale late on the
// second in Moscow is on the second for a Moscow operator and on the second in
// UTC too — but one at 02:00 UTC on the third is on the third in UTC and the
// third in Moscow as well, so the case that distinguishes them is a sale in the
// UTC evening read from a zone east of it.
func TestDaysAreBucketedInTheRequestedTimezone(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := newOperations(t, harness)

	customer := seedCustomer(ctx, t, harness, "sales-tz@example.test")
	// 22:00 UTC on the 2nd is 01:00 on the 3rd in Moscow.
	paidAt := time.Date(2026, 3, 2, 22, 0, 0, 0, time.UTC)
	seedSettledOrder(ctx, t, harness, customer, "purchase", "USD", 1000, paidAt)

	since, until := paidAt.Add(-48*time.Hour), paidAt.Add(48*time.Hour)

	utc, err := service.SalesReport(ctx, since, until, "UTC", "")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(utc.ByDay) != 1 || utc.ByDay[0].Day != "2026-03-02" {
		t.Fatalf("UTC bucketed the sale as %+v", utc.ByDay)
	}

	moscow, err := service.SalesReport(ctx, since, until, "Europe/Moscow", "")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(moscow.ByDay) != 1 || moscow.ByDay[0].Day != "2026-03-03" {
		t.Fatalf(
			"a Moscow operator was shown the sale on %+v; the day boundary "+
				"ignored the timezone", moscow.ByDay,
		)
	}
}

// Helpers. They write rows directly rather than going through the commerce
// service, because what is under test is the reporting query rather than the
// purchase path, and a full purchase needs a plan, a provider, and a Remnawave
// user this test has no use for.

func seedCustomer(
	ctx context.Context, t *testing.T, harness *harness, email string,
) string {
	t.Helper()
	var id string
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO users (locale, timezone) VALUES ('en', 'UTC') RETURNING id`,
	).Scan(&id); err != nil {
		t.Fatalf("seed customer %s: %v", email, err)
	}
	return id
}

func seedSettledOrder(
	ctx context.Context, t *testing.T, harness *harness,
	customerID, operation, currency string, amount int64, paidAt time.Time,
) {
	t.Helper()
	if _, err := harness.pool.Exec(ctx, `
		INSERT INTO orders (
			user_id, state, operation, currency, subtotal_minor, external_minor,
			paid_minor, idempotency_key, paid_at
		) VALUES ($1, 'paid', $2, $3, $4, $4, $4, $5, $6)`,
		customerID, operation, currency, amount,
		"seed-"+operation+"-"+currency+"-"+strings.ReplaceAll(paidAt.String(), " ", "_"),
		paidAt,
	); err != nil {
		t.Fatalf("seed order: %v", err)
	}
}

func seedDraftOrder(
	ctx context.Context, t *testing.T, harness *harness,
	customerID, currency string, amount int64,
) {
	t.Helper()
	if _, err := harness.pool.Exec(ctx, `
		INSERT INTO orders (
			user_id, state, operation, currency, subtotal_minor, external_minor,
			idempotency_key
		) VALUES ($1, 'draft', 'purchase', $2, $3, $3, 'seed-draft')`,
		customerID, currency, amount,
	); err != nil {
		t.Fatalf("seed draft: %v", err)
	}
}
