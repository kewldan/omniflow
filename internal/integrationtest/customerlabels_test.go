//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/botapp"
)

// The bot keeps the Telegram name and @username it sees on the customer's
// identity, and the panel names customers by them; a customer without a
// Telegram identity comes back without a name, for the identifier fallback.
func TestCustomersAreNamedByTheirTelegramProfile(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store, err := botapp.NewPostgresStore(ctx, harness.url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	named := harness.customer(ctx, t)
	bare := harness.customer(ctx, t)
	if _, err = harness.pool.Exec(ctx, `INSERT INTO identities (user_id, provider, provider_subject, verified_at)
		VALUES ($1::uuid, 'telegram', '424242', now())`, named); err != nil {
		t.Fatal(err)
	}
	if err = store.RecordTelegramProfile(ctx, 424242, "ivan", "Иван", "Петров"); err != nil {
		t.Fatal(err)
	}
	// A rename is written; dropping the @username removes it.
	if err = store.RecordTelegramProfile(ctx, 424242, "", "Иван", ""); err != nil {
		t.Fatal(err)
	}

	labels, err := newOperations(t, harness).CustomerLabels(ctx, []string{named, bare, "not-a-uuid"})
	if err != nil || len(labels) != 2 {
		t.Fatalf("labels %+v %v", labels, err)
	}
	for _, label := range labels {
		switch label.ID {
		case named:
			if label.Name != "Иван" || label.Username != "" || label.TelegramID == nil || *label.TelegramID != 424242 {
				t.Fatalf("named customer: %+v", label)
			}
		case bare:
			if label.Name != "" || label.TelegramID != nil {
				t.Fatalf("customer without Telegram: %+v", label)
			}
		}
	}
}

// Every count beside the menu runs against the real schema, and an area the
// caller did not ask for is not counted.
func TestNavigationCountsRun(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)
	counts, err := operations.NavigationCounts(ctx, []string{"support", "shop", "payouts", "finance", "risk", "system", "unknown"}, "00000000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 6 {
		t.Fatalf("counts %v", counts)
	}
	if _, err = harness.pool.Exec(ctx, `INSERT INTO app_errors (fingerprint, process, message, source) VALUES ('f', 'api', 'boom', 'x')`); err != nil {
		t.Fatal(err)
	}
	counts, err = operations.NavigationCounts(ctx, []string{"system"}, "00000000-0000-0000-0000-000000000001")
	if err != nil || counts["system"] != 1 || len(counts) != 1 {
		t.Fatalf("system counts %v %v", counts, err)
	}
}
