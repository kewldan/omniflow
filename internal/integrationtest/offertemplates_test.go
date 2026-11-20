//go:build integration

package integrationtest

import (
	"context"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// A template sent to a segment offers it to each customer once; with a timer
// the discount does not apply until the customer takes the offer, taking it
// starts the timer and pays the bonus once.
func TestOfferTemplatesSendToASegmentWithTimerAndBonus(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	panel := newOperations(t, harness)
	actor := harness.operator(ctx, t, "offers@omniflow.test")

	versionID := harness.catalog(ctx, t, "offer-plan", 10000)
	harness.promotion(ctx, t, "OFFERTEN", harness.planIDOf(ctx, t, versionID), 1)
	var promotionID string
	if err := harness.pool.QueryRow(ctx, `SELECT id::text FROM promotions WHERE code = 'offerten'`).Scan(&promotionID); err != nil {
		t.Fatal(err)
	}
	timer := int32(48)
	template, err := panel.SaveOfferTemplate(ctx, panelpg.OfferTemplate{
		Code: "comeback", PromotionID: promotionID, TitleRU: "Вернитесь", TitleEN: "Come back",
		ValidHours: 72, ActivationHours: &timer, BonusMinor: 3000, BonusCurrency: "RUB",
	}, actor)
	if err != nil {
		t.Fatalf("save template: %v", err)
	}
	first := harness.customer(ctx, t)
	second := harness.customer(ctx, t)
	segment, err := panel.SaveSegment(ctx, panelpg.AudienceSegment{
		Code: "two-customers", NameEN: "Two", NameRU: "Двое",
		Filters: map[string]any{"customerIds": []any{first, second}},
	}, actor)
	if err != nil {
		t.Fatalf("segment: %v", err)
	}
	if offered, err := panel.SendOfferTemplate(ctx, template.ID, segment.ID, actor); err != nil || offered != 2 {
		t.Fatalf("send: %d (%v), want 2", offered, err)
	}
	if offered, err := panel.SendOfferTemplate(ctx, template.ID, segment.ID, actor); err != nil || offered != 0 {
		t.Fatalf("second send: %d (%v), want nobody twice", offered, err)
	}

	input := commercepg.CreateOrderInput{CustomerID: first, PlanVersionID: versionID, Currency: "RUB", Operation: "purchase", IdempotencyKey: "offer-1"}
	if quote, err := store.PreviewOrder(ctx, input); err != nil || quote.DiscountMinor != 0 {
		t.Fatalf("before taking: %+v (%v), want no discount", quote, err)
	}
	var offerID string
	if err := harness.pool.QueryRow(ctx, `SELECT id::text FROM personal_offers WHERE user_id = $1::uuid`, first).Scan(&offerID); err != nil {
		t.Fatal(err)
	}
	activation, err := store.ActivatePersonalOffer(ctx, first, offerID)
	if err != nil || !activation.Activated || activation.BonusMinor != 3000 {
		t.Fatalf("activation: %+v (%v)", activation, err)
	}
	if left := time.Until(activation.ExpiresAt); left < 47*time.Hour || left > 49*time.Hour {
		t.Fatalf("timer ends in %v, want about 48 h", left)
	}
	if again, err := store.ActivatePersonalOffer(ctx, first, offerID); err != nil || again.Activated {
		t.Fatalf("second activation: %+v (%v)", again, err)
	}
	if balance := harness.walletBalance(ctx, t, first); balance != 3000 {
		t.Fatalf("bonus balance %d, want 3000 once", balance)
	}
	input.SkipWallet = true
	if quote, err := store.PreviewOrder(ctx, input); err != nil || quote.DiscountMinor != 1000 {
		t.Fatalf("after taking: %+v (%v), want the offer's 10%%", quote, err)
	}
	if _, err := store.ActivatePersonalOffer(ctx, second, offerID); err == nil {
		t.Fatal("another customer activated somebody else's offer")
	}
}
