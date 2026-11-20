//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/panelpg"
)

// The live stream's counters move when something in their area changes.
func TestLiveVersionsMove(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service, err := panelpg.New(harness.pool, operationsKey, panelpg.Options{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := service.LiveVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	customer := harness.customer(ctx, t)
	harness.ticket(ctx, t, customer, "Не работает")
	after, err := service.LiveVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after["support"] == before["support"] {
		t.Fatalf("support version did not move: %v → %v", before, after)
	}
}
