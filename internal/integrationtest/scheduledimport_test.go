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

// A panel user the operator excluded by username or squad never becomes a
// customer, in the scheduled import or an uploaded file, and is staged as
// skipped with the reason so the preview shows why.
func TestImportSkipsExcludedPanelUsers(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	if _, err := harness.pool.Exec(ctx, `INSERT INTO installation_settings (section, document) VALUES ('remnawave',
		'{"importExcludedUsernames": "Bridge_User\n@relay", "importExcludedSquads": "chains\n7d0c6b0e-0000-4000-8000-0000000000aa"}')
		ON CONFLICT (section) DO UPDATE SET document = EXCLUDED.document`); err != nil {
		t.Fatalf("save exclusions: %v", err)
	}
	telegram := int64(820001)
	service := importservice.New(harness.pool, panelUsers{
		{ID: 9101, Username: "bridge_user", Status: "ACTIVE"},
		{ID: 9102, Username: "relay", Status: "ACTIVE"},
		{ID: 9103, Username: "hop", Status: "ACTIVE", ActiveInternalSquads: []remnawave.InternalSquad{{UUID: "1b1b1b1b-0000-4000-8000-000000000001", Name: "Chains"}}},
		{ID: 9104, Username: "hop2", Status: "ACTIVE", ActiveInternalSquads: []remnawave.InternalSquad{{UUID: "7d0c6b0e-0000-4000-8000-0000000000aa", Name: "Other"}}},
		{ID: 9105, Username: "customer", Status: "ACTIVE", TelegramID: &telegram},
	})
	run, err := service.RunAll(ctx)
	if err != nil || run.Status != "completed" {
		t.Fatalf("run: %s (%v)", run.Status, err)
	}
	var mapped []int64
	rows, err := harness.pool.Query(ctx, `SELECT remnawave_id FROM remnawave_users WHERE remnawave_id BETWEEN 9101 AND 9105 ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		mapped = append(mapped, id)
	}
	rows.Close()
	if len(mapped) != 1 || mapped[0] != 9105 {
		t.Fatalf("mapped %v, want only the real customer", mapped)
	}
	var skipped int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM customer_import_items
		WHERE status = 'skipped' AND validation_errors ? 'excluded'`).Scan(&skipped); err != nil || skipped != 4 {
		t.Fatalf("staged %d excluded users (%v), want 4", skipped, err)
	}

	rowsIn := []importservice.FileRow{{RemnawaveID: 9201, Username: "BRIDGE_USER"}, {RemnawaveID: 9202, Username: "filed"}}
	file, err := service.PreviewFile(ctx, rowsIn, nil)
	if err != nil || file.ValidCount != 1 {
		t.Fatalf("file preview: valid %d (%v), want the excluded username left out", file.ValidCount, err)
	}
}
