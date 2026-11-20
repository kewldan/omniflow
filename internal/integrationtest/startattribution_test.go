//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// A /start link with UTM tags attributes the customer, the first tags win, and
// their orders are attributed in order_attributions for the conversion export.
func TestStartLinkTagsAttributeOrders(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	store := harness.commerceStore(t, testOptions())

	start := func(text string) {
		flow.deliver(ctx, &models.Update{Message: &models.Message{
			ID:   int(flow.update + 1),
			From: &models.User{ID: 712001, LanguageCode: "en", FirstName: "Ann"},
			Chat: models.Chat{ID: 712001, Type: models.ChatTypePrivate},
			Text: text,
		}})
	}
	start("/start utm_vk__cpc__black_friday")
	start("/start utm_other")
	customer := flow.customerOf(ctx, t, 712001)

	versionID := harness.catalog(ctx, t, "tags-plan", 10000)
	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: customer, PlanVersionID: versionID, Currency: "RUB", Operation: "purchase", IdempotencyKey: "tags-1",
	})
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	var source, medium, campaign string
	if err := harness.pool.QueryRow(ctx, `SELECT utm_source, utm_medium, utm_campaign FROM order_attributions WHERE order_id = $1`,
		order.ID).Scan(&source, &medium, &campaign); err != nil {
		t.Fatalf("order attribution: %v", err)
	}
	if source != "vk" || medium != "cpc" || campaign != "black_friday" {
		t.Fatalf("attribution = %s/%s/%s, want the first link's tags", source, medium, campaign)
	}
}
