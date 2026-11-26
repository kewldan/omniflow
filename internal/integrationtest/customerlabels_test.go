//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/botapp"
	"github.com/omniflow/omniflow/internal/panelpg"
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

// The hover card names the customer, states a time-limited ban with its end,
// lists the subscriptions, and adds up only money that moved: a settled
// order's payment less its refund, per currency, and nothing for an order
// that was never paid.
func TestCustomerCardSummarisesTheCustomer(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	customer := harness.customer(ctx, t)
	harness.exec(ctx, t, `INSERT INTO identities (user_id, provider, provider_subject, verified_at, metadata)
		VALUES ($1::uuid, 'telegram', '515151', now(), '{"firstName":"Анна","username":"anna"}')`, customer)
	harness.exec(ctx, t, `UPDATE users SET status = 'suspended', suspended_at = now(),
		suspension_ends_at = now() + interval '3 days' WHERE id = $1::uuid`, customer)
	harness.exec(ctx, t, `INSERT INTO subscriptions (user_id, slot, label) VALUES ($1::uuid, 1, 'Phone')`, customer)
	harness.exec(ctx, t, `INSERT INTO orders (user_id, state, operation, currency, subtotal_minor, external_minor, paid_minor, refunded_minor, idempotency_key, paid_at) VALUES
		($1::uuid, 'fulfilled', 'purchase', 'RUB', 10000, 10000, 10000, 0, 'card-1', now()),
		($1::uuid, 'partially_refunded', 'purchase', 'RUB', 10000, 10000, 10000, 3000, 'card-2', now()),
		($1::uuid, 'paid', 'purchase', 'USD', 500, 500, 500, 0, 'card-3', now())`, customer)
	harness.exec(ctx, t, `INSERT INTO orders (user_id, state, operation, currency, subtotal_minor, external_minor, paid_minor, idempotency_key)
		VALUES ($1::uuid, 'pending', 'purchase', 'RUB', 9900, 9900, 0, 'card-4')`, customer)

	card, err := newOperations(t, harness).CustomerCard(ctx, customer)
	if err != nil {
		t.Fatal(err)
	}
	if card.Name != "Анна" || card.Username != "anna" || card.TelegramID == nil || *card.TelegramID != 515151 {
		t.Fatalf("identity: %+v", card)
	}
	if card.Status != "suspended" || card.SuspensionEndsAt == nil || card.CreatedAt.IsZero() {
		t.Fatalf("status: %+v", card)
	}
	if len(card.Subscriptions) != 1 || card.Subscriptions[0].Label != "Phone" || card.Subscriptions[0].Status != "active" {
		t.Fatalf("subscriptions: %+v", card.Subscriptions)
	}
	want := []panelpg.CustomerCardSpend{
		{Currency: "RUB", SpentMinor: 17000, OrderCount: 2},
		{Currency: "USD", SpentMinor: 500, OrderCount: 1},
	}
	if len(card.Spent) != len(want) || card.Spent[0] != want[0] || card.Spent[1] != want[1] {
		t.Fatalf("spent: %+v", card.Spent)
	}

	// A suspension that is lifted stops being reported, whatever the column
	// still holds.
	harness.exec(ctx, t, `UPDATE users SET status = 'active', suspended_at = NULL WHERE id = $1::uuid`, customer)
	if card, err = newOperations(t, harness).CustomerCard(ctx, customer); err != nil || card.SuspensionEndsAt != nil {
		t.Fatalf("lifted: %+v %v", card, err)
	}
	if _, err = newOperations(t, harness).CustomerCard(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, panelpg.ErrNotFound) {
		t.Fatalf("unknown customer: %v", err)
	}
}
