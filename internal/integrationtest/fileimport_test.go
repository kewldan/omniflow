//go:build integration

package integrationtest

import (
	"context"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/importservice"
)

// An uploaded CSV is staged with the same duplicate detection as a
// Remnawave preview, and applying it twice creates each customer once.
func TestFileImportStagesAndApplies(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := importservice.New(harness.pool, nil)
	rows, invalid, err := importservice.ParseFile(strings.NewReader(
		"remnawave_id,telegram_id,username\n501,9001,alice\n502,9001,bob\n503,,carol\nx,9003,dave\n"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := service.PreviewFile(ctx, rows, invalid)
	if err != nil {
		t.Fatal(err)
	}
	if run.Source != "file" || run.Status != "ready" || run.TotalCount != 4 || run.ValidCount != 2 || run.ConflictCount != 1 || run.InvalidCount != 1 {
		t.Fatalf("staged = %+v", run)
	}
	id := uuidText(run.ID)
	for range 2 {
		if run, err = service.Apply(ctx, id, 100); err != nil && run.Status != "completed" {
			if !strings.Contains(err.Error(), "not ready") {
				t.Fatalf("apply: %v", err)
			}
		}
	}
	var mapped int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM remnawave_users WHERE remnawave_id IN (501, 502, 503)`).Scan(&mapped); err != nil || mapped != 2 {
		t.Fatalf("mapped customers = %d, %v", mapped, err)
	}
}
