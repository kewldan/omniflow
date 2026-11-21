//go:build integration

package integrationtest

import (
	"context"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/panelpg"
)

// TestDashboardSeriesAddUpToTheirTiles holds the dashboard's daily series to
// the figures they are drawn beside. Each series is the tile's own window cut
// into 24-hour buckets, so its buckets must add up to the tile, and a record
// must land in the bucket its age says it belongs to.
func TestDashboardSeriesAddUpToTheirTiles(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)

	// Customers two hours, five days, and forty days old: the last is outside
	// the thirty-day window and must appear in no bucket.
	for _, age := range []string{"2 hours", "5 days 1 hour", "40 days"} {
		id := harness.customer(ctx, t)
		if _, err := harness.pool.Exec(ctx,
			`UPDATE users SET created_at = now() - $2::interval WHERE id = $1::uuid`, id, age); err != nil {
			t.Fatalf("age customer: %v", err)
		}
	}

	// Two active entitlements ending in 36 hours and in ten and a half days.
	// Each comes with a paid order settled now, which is the revenue series.
	owner := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "dashboard-series", 1000)
	for slot, ends := range []string{"36 hours", "10 days 12 hours"} {
		subscriptionID := harness.seedSubscription(ctx, t, owner, slot+1)
		harness.seedSubscriptionEntitlement(ctx, t, owner, subscriptionID, planVersionID, "active", ends)
	}

	var orderID string
	if err := harness.pool.QueryRow(ctx, `SELECT id::text FROM orders LIMIT 1`).Scan(&orderID); err != nil {
		t.Fatalf("read order: %v", err)
	}
	now := time.Now()
	seedIntent(ctx, t, harness, orderID, "manual", "succeeded", 1000, now.Add(-3*time.Hour))
	seedIntent(ctx, t, harness, orderID, "manual", "failed", 1000, now.Add(-50*time.Hour))

	dashboard, err := operations.Dashboard(ctx, panelpg.DashboardWindow(30))
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}

	metrics := map[string]panelpg.Metric{}
	for _, group := range [][]panelpg.Metric{dashboard.Customers, dashboard.Subscriptions, dashboard.Payments} {
		for _, metric := range group {
			metrics[metric.Key] = metric
		}
	}

	// History buckets are oldest first, so bucket b (b days ago) is at 29-b.
	// Upcoming buckets are soonest first, so bucket b is at b.
	cases := []struct {
		key     string
		kind    string
		buckets map[int]int64
	}{
		{"newCustomers", panelpg.SeriesHistory, map[int]int64{29: 2, 24: 1}},
		{"paymentsSucceeded", panelpg.SeriesHistory, map[int]int64{29: 1}},
		{"paymentsFailed", panelpg.SeriesHistory, map[int]int64{27: 1}},
		{"renewalsDue", panelpg.SeriesUpcoming, map[int]int64{1: 1, 10: 1}},
	}
	for _, want := range cases {
		metric, ok := metrics[want.key]
		if !ok {
			t.Fatalf("%s: missing from the dashboard", want.key)
		}
		if metric.SeriesKind != want.kind || len(metric.Series) != 30 {
			t.Fatalf("%s: %q series of %d points, want %q of 30", want.key, metric.SeriesKind, len(metric.Series), want.kind)
		}
		var sum int64
		for index, point := range metric.Series {
			sum += point.Value
			if point.Value != want.buckets[index] {
				t.Errorf("%s: point %d is %d, want %d", want.key, index, point.Value, want.buckets[index])
			}
		}
		if sum != metric.Value {
			t.Errorf("%s: series adds up to %d, the tile says %d", want.key, sum, metric.Value)
		}
	}

	// A point-in-time total has no history to draw.
	if series := metrics["activeEntitlements"].Series; len(series) != 0 {
		t.Errorf("activeEntitlements carries a %d-point series; its history is not recorded", len(series))
	}

	if len(dashboard.Revenue) != 1 {
		t.Fatalf("%d revenue lines, want 1", len(dashboard.Revenue))
	}
	line := dashboard.Revenue[0]
	var paid int64
	for _, point := range line.PaidSeries {
		paid += point.Value
	}
	if len(line.PaidSeries) != 30 || paid != line.PaidMinor || line.PaidSeries[29].Value != line.PaidMinor {
		t.Fatalf("revenue series %d points adding to %d (last %d), tile %d",
			len(line.PaidSeries), paid, line.PaidSeries[29].Value, line.PaidMinor)
	}
}
