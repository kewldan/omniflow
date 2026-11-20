//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/importservice"
	"github.com/omniflow/omniflow/internal/remnawave"
)

type panelUsers []remnawave.User

func (users panelUsers) ListUsers(_ context.Context, start, size int) ([]remnawave.User, int, error) {
	end := min(start+size, len(users))
	if start >= end {
		return nil, len(users), nil
	}
	return users[start:end], len(users), nil
}

// The scheduled import makes customers of panel users Omniflow does not know,
// and running it again changes nothing: a user it already mapped is a
// conflict, not a second customer.
func TestScheduledImportAdoptsPanelUsersOnce(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	first, second := int64(810001), int64(810002)
	service := importservice.New(harness.pool, panelUsers{
		{ID: 9001, Username: "made_in_panel", Status: "ACTIVE", TelegramID: &first},
		{ID: 9002, Username: "another", Status: "ACTIVE", TelegramID: &second},
	})
	run, err := service.RunAll(ctx)
	if err != nil || run.Status != "completed" {
		t.Fatalf("first run: %s (%v)", run.Status, err)
	}
	count := func() int {
		var mapped int
		if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM remnawave_users WHERE remnawave_id IN (9001, 9002)`).Scan(&mapped); err != nil {
			t.Fatal(err)
		}
		return mapped
	}
	if count() != 2 {
		t.Fatalf("mapped %d panel users, want 2", count())
	}
	if _, err = service.RunAll(ctx); err != nil {
		t.Fatal(err)
	}
	if count() != 2 {
		t.Fatalf("a second run mapped %d", count())
	}
}
