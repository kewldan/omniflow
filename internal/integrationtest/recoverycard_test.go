//go:build integration

package integrationtest

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/accountpg"
	"github.com/omniflow/omniflow/internal/botapp"
)

// A subscription that ended two days ago is offered back on the dashboard,
// with the win-back discount the customer already holds; one that ended
// outside the window is not.
func TestRecoveryCardOffersARecentlyEndedSubscription(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	customer := harness.customer(ctx, t)
	subscription := harness.seedSubscription(ctx, t, customer, 1)
	harness.seedSubscriptionEntitlement(ctx, t, customer, subscription,
		harness.catalog(ctx, t, "recovery-plan", 10000), "expired", "-2 days")
	bot, err := botapp.NewPostgresStoreWithPool(harness.pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = bot.IssueWinbackOffer(ctx, customer, "00000000-0000-0000-0000-000000000002",
		botapp.WinbackWave{Number: 2, AfterDays: 2, Percent: 10, ValidFor: 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	account, err := accountpg.New(harness.pool, accountpg.UnavailablePanel{}, accountpg.Options{
		Logger: slog.New(slog.DiscardHandler), RecoveryWindow: 7 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	overview, err := account.Overview(ctx, customer, "en")
	if err != nil {
		t.Fatal(err)
	}
	recovery, ok := account.Recovery(ctx, customer, overview.Subscriptions)
	if !ok || recovery.SubscriptionID != subscription || recovery.OfferKind != "percent" || recovery.OfferValue != 10 {
		t.Fatalf("recovery = %+v (%v), phases %+v", recovery, ok, overview.Subscriptions)
	}
	narrow, _ := accountpg.New(harness.pool, accountpg.UnavailablePanel{}, accountpg.Options{
		Logger: slog.New(slog.DiscardHandler), RecoveryWindow: time.Hour,
	})
	if _, ok := narrow.Recovery(ctx, customer, overview.Subscriptions); ok {
		t.Fatal("a subscription that ended outside the window is not offered back")
	}
}
