package commerce

import (
	"errors"
	"testing"
	"time"
)

func TestFullPriceProrationIgnoresRemainingTime(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	starts := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	ends := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	charge, err := PriceAddon(30000, 1, 1, ProrationFullPrice, now, starts, ends)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if charge.ChargedMinor != 30000 {
		t.Fatalf("full price must not be reduced: %d", charge.ChargedMinor)
	}
}

func TestRemainingPeriodProrationScalesAndRoundsUp(t *testing.T) {
	t.Parallel()
	starts := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	ends := starts.Add(30 * 24 * time.Hour)
	// Ten of thirty days left: a third of the price, rounded up.
	charge, err := PriceAddon(30001, 1, 1, ProrationRemainingPeriod, ends.Add(-10*24*time.Hour), starts, ends)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if charge.ChargedMinor != 10001 {
		t.Fatalf("expected 10001 minor units, got %d", charge.ChargedMinor)
	}
}

func TestDailyRateProrationChargesWholeRemainingDays(t *testing.T) {
	t.Parallel()
	starts := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	ends := starts.Add(30 * 24 * time.Hour)
	// 30000 over 30 days is 1000 a day; 9.5 days left rounds to 10 days.
	charge, err := PriceAddon(30000, 1, 1, ProrationDailyRate, ends.Add(-9*24*time.Hour-12*time.Hour), starts, ends)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if charge.ChargedMinor != 10000 {
		t.Fatalf("expected 10000 minor units, got %d", charge.ChargedMinor)
	}
}

// A prorated add-on may never cost more than the catalog price it is prorated
// from, whatever the rounding does.
func TestProrationNeverExceedsTheCatalogPrice(t *testing.T) {
	t.Parallel()
	starts := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	ends := starts.Add(7 * 24 * time.Hour)
	for _, rule := range []Proration{ProrationRemainingPeriod, ProrationDailyRate} {
		charge, err := PriceAddon(999, 1, 1, rule, starts.Add(time.Minute), starts, ends)
		if err != nil {
			t.Fatalf("unexpected error for %s: %v", rule, err)
		}
		if charge.ChargedMinor > 999 {
			t.Fatalf("%s charged %d, above the catalog price", rule, charge.ChargedMinor)
		}
	}
}

// Buying before the period starts, or after it ended, falls back to full price
// rather than giving the add-on away.
func TestProrationFallsBackToFullPriceOutsideThePeriod(t *testing.T) {
	t.Parallel()
	starts := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	ends := starts.Add(30 * 24 * time.Hour)
	for _, now := range []time.Time{starts.Add(-time.Hour), ends.Add(time.Hour)} {
		charge, err := PriceAddon(30000, 1, 1, ProrationRemainingPeriod, now, starts, ends)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if charge.ChargedMinor != 30000 {
			t.Fatalf("expected the full price outside the period, got %d", charge.ChargedMinor)
		}
	}
}

func TestPriceAddonRejectsAnInvalidQuantityOrRule(t *testing.T) {
	t.Parallel()
	now := time.Now()
	if _, err := PriceAddon(1000, 0, 5, ProrationFullPrice, now, now, now.Add(time.Hour)); !errors.Is(err, ErrAddonQuantity) {
		t.Fatalf("expected a quantity error, got %v", err)
	}
	if _, err := PriceAddon(1000, 6, 5, ProrationFullPrice, now, now, now.Add(time.Hour)); !errors.Is(err, ErrAddonQuantity) {
		t.Fatalf("expected a quantity ceiling error, got %v", err)
	}
	if _, err := PriceAddon(1000, 1, 5, "monthly", now, now, now.Add(time.Hour)); !errors.Is(err, ErrUnsupportedProration) {
		t.Fatalf("expected an unsupported-rule error, got %v", err)
	}
}

func TestCapacityMultipliesUnitsButNotSquads(t *testing.T) {
	t.Parallel()
	capacity := Capacity(1024, 2, 3, []string{"squad-a", "squad-b"})
	if capacity.TrafficBytes != 3072 {
		t.Fatalf("expected 3072 bytes, got %d", capacity.TrafficBytes)
	}
	if capacity.DeviceSlots != 6 {
		t.Fatalf("expected 6 device slots, got %d", capacity.DeviceSlots)
	}
	if len(capacity.SquadIDs) != 2 {
		t.Fatalf("squads are a set and must not be multiplied: %v", capacity.SquadIDs)
	}
}
