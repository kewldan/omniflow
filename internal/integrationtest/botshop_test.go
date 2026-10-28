//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/botapp"
	"github.com/omniflow/omniflow/internal/retention"
)

// shopReviewButton finds the button on a review screen that starts with the
// given action, and returns its callback data.
func shopReviewButton(t *testing.T, screen botRequest, action string) string {
	t.Helper()
	for _, data := range screen.callbacks(t) {
		if strings.HasPrefix(data, "act:"+action+":") {
			return data
		}
	}
	t.Fatalf("the review screen has no %s button: %v", action, screen.callbacks(t))
	return ""
}

// TestBotShopReviewFitsAndBuysForALongRecipient types a recipient long enough
// that the review buttons used to exceed Telegram's 64-byte callback limit.
// Telegram refused the whole screen, so a customer who typed the username got
// no answer at all. The review now renders, and its buy button orders for the
// recipient it showed — even after a second review for somebody else was
// opened in between.
func TestBotShopReviewFitsAndBuysForALongRecipient(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	productID := harness.goodsProduct(ctx, t)
	const buyer = int64(700400)
	buyerID := flow.customerOf(ctx, t, buyer)
	const first, second = "a_rather_long_recipient_name", "another_recipient_name"

	flow.tap(ctx, buyer, 20, "act:shop-other:"+productID)
	flow.say(ctx, buyer, "@"+first)
	firstReview := flow.telegram.lastScreen(t)
	if !strings.Contains(firstReview.Fields["text"], first) {
		t.Fatalf("the review does not restate the recipient: %q", firstReview.Fields["text"])
	}
	buy := shopReviewButton(t, firstReview, "shop-buy")

	flow.tap(ctx, buyer, 21, "act:shop-other:"+productID)
	flow.say(ctx, buyer, second)
	if !strings.Contains(flow.telegram.lastScreen(t).Fields["text"], second) {
		t.Fatal("the second review was not shown")
	}
	if len(flow.telegram.oversized) > 0 {
		t.Fatalf("Telegram would have refused callbacks: %v", flow.telegram.oversized)
	}

	// The first screen's button still buys for the first recipient.
	flow.tap(ctx, buyer, 20, buy)
	var recipient string
	if err := harness.pool.QueryRow(ctx, `SELECT g.recipient_username FROM goods_orders g
		JOIN orders o ON o.id = g.order_id WHERE o.user_id = $1::uuid`, buyerID).Scan(&recipient); err != nil {
		t.Fatalf("the buy button opened no order: %v", err)
	}
	if recipient != first {
		t.Fatalf("the order is for %q, want the recipient the screen showed (%q)", recipient, first)
	}
}

// TestRetentionSweepRemovesExpiredShopReviews holds the review table to its
// one-day window: an expired review is deleted, a live one is kept, and a
// review past its window is refused to its own customer as well.
func TestRetentionSweepRemovesExpiredShopReviews(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	productID := harness.goodsProduct(ctx, t)
	customerID := flow.customerOf(ctx, t, 700700)

	var expired string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO bot_shop_reviews (user_id, product_id, recipient, expires_at)
		VALUES ($1::uuid, $2::uuid, 'old_recipient', now() - interval '1 minute') RETURNING id::text`,
		customerID, productID).Scan(&expired); err != nil {
		t.Fatalf("seed an expired review: %v", err)
	}
	live, err := flow.store.OpenShopReview(ctx, customerID, productID, "live_recipient")
	if err != nil {
		t.Fatalf("open a review: %v", err)
	}
	if _, err = flow.store.ShopReviewFor(ctx, customerID, expired); !errors.Is(err, botapp.ErrShopReviewGone) {
		t.Fatalf("an expired review was still served: %v", err)
	}

	result := retention.New(harness.pool, slog.New(slog.DiscardHandler), retention.Config{
		Outbox: 7 * 24 * time.Hour, Telemetry: 30 * 24 * time.Hour, Drift: 90 * 24 * time.Hour,
	}).Sweep(ctx)
	// Opening the live review already cleared this customer's expired one, so
	// the sweep finds nothing of theirs; it is the customers who never come
	// back that the sweep is for.
	var remaining []string
	rows, err := harness.pool.Query(ctx, `SELECT id::text FROM bot_shop_reviews`)
	if err != nil {
		t.Fatalf("read reviews: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan review: %v", err)
		}
		remaining = append(remaining, id)
	}
	if len(remaining) != 1 || remaining[0] != live {
		t.Fatalf("reviews after the sweep = %v (swept %d), want only the live one", remaining, result.ShopReviews)
	}

	if _, err := harness.pool.Exec(ctx, `INSERT INTO bot_shop_reviews (user_id, product_id, recipient, expires_at)
		VALUES ($1::uuid, $2::uuid, 'abandoned', now() - interval '1 minute')`, customerID, productID); err != nil {
		t.Fatalf("seed an abandoned review: %v", err)
	}
	result = retention.New(harness.pool, slog.New(slog.DiscardHandler), retention.Config{
		Outbox: 7 * 24 * time.Hour, Telemetry: 30 * 24 * time.Hour, Drift: 90 * 24 * time.Hour,
	}).Sweep(ctx)
	if result.ShopReviews != 1 {
		t.Fatalf("the sweep removed %d expired reviews, want 1", result.ShopReviews)
	}
}

// TestBotShopReviewOfAnotherCustomerBuysNothing taps a review identifier that
// belongs to somebody else. It is refused like an expired one: back to the
// catalogue, no order.
func TestBotShopReviewOfAnotherCustomerBuysNothing(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	productID := harness.goodsProduct(ctx, t)
	const owner, stranger = int64(700500), int64(700600)
	flow.customerOf(ctx, t, owner)
	strangerID := flow.customerOf(ctx, t, stranger)

	flow.tap(ctx, owner, 30, "act:shop-other:"+productID)
	flow.say(ctx, owner, "owners_friend")
	buy := shopReviewButton(t, flow.telegram.lastScreen(t), "shop-buy")

	flow.tap(ctx, stranger, 31, buy)
	var orders int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*)::integer FROM orders WHERE user_id = $1::uuid`, strangerID).Scan(&orders); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if orders != 0 {
		t.Fatalf("another customer's review opened %d orders", orders)
	}
}
