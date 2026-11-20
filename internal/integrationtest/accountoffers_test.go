//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// The customer panel lists a customer's personal offers, takes one (timer and
// bonus, once), and dismisses one, which resets the discount: checkout stops
// applying it at once. Nobody can take or dismiss another customer's offer.
func TestCustomerPanelTakesAndDismissesPersonalOffers(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	panel := newOperations(t, harness)
	checkout := newAccountCheckout(t, harness, store)
	actor := harness.operator(ctx, t, "web-offers@omniflow.test")

	versionID := harness.catalog(ctx, t, "web-offer-plan", 10000)
	harness.promotion(ctx, t, "WEBTEN", harness.planIDOf(ctx, t, versionID), 1)
	var promotionID string
	if err := harness.pool.QueryRow(ctx, `SELECT id::text FROM promotions WHERE code = 'webten'`).Scan(&promotionID); err != nil {
		t.Fatal(err)
	}
	template, err := panel.SaveOfferTemplate(ctx, panelpg.OfferTemplate{
		Code: "web-comeback", PromotionID: promotionID, TitleRU: "Вернитесь", TitleEN: "Come back",
		ValidHours: 72, BonusMinor: 2000, BonusCurrency: "RUB",
	}, actor)
	if err != nil {
		t.Fatalf("save template: %v", err)
	}
	customer := harness.customer(ctx, t)
	stranger := harness.customer(ctx, t)
	segment, err := panel.SaveSegment(ctx, panelpg.AudienceSegment{
		Code: "web-one", NameEN: "One", NameRU: "Один",
		Filters: map[string]any{"customerIds": []any{customer}},
	}, actor)
	if err != nil {
		t.Fatalf("segment: %v", err)
	}
	if offered, err := panel.SendOfferTemplate(ctx, template.ID, segment.ID, actor); err != nil || offered != 1 {
		t.Fatalf("send: %d (%v)", offered, err)
	}

	offers, err := checkout.Offers(ctx, customer, "en")
	if err != nil || len(offers) != 1 || offers[0].Title != "Come back" || offers[0].ActivatedAt != nil {
		t.Fatalf("offers: %+v (%v)", offers, err)
	}
	if others, err := checkout.Offers(ctx, stranger, "en"); err != nil || len(others) != 0 {
		t.Fatalf("a stranger sees %d offers (%v)", len(others), err)
	}
	offerID := offers[0].ID

	if _, err = checkout.TakeOffer(ctx, stranger, offerID); !errors.Is(err, accountcheckout.ErrOfferNotFound) {
		t.Fatalf("a stranger took the offer: %v", err)
	}
	taken, err := checkout.TakeOffer(ctx, customer, offerID)
	if err != nil || !taken.Activated || taken.BonusMinor != 2000 {
		t.Fatalf("take: %+v (%v)", taken, err)
	}
	if again, err := checkout.TakeOffer(ctx, customer, offerID); err != nil || again.Activated {
		t.Fatalf("taking twice: %+v (%v)", again, err)
	}

	input := commercepg.CreateOrderInput{CustomerID: customer, PlanVersionID: versionID, Currency: "RUB", Operation: "purchase", IdempotencyKey: "web-offer", SkipWallet: true}
	if quote, err := store.PreviewOrder(ctx, input); err != nil || quote.DiscountMinor == 0 {
		t.Fatalf("a taken offer must discount checkout: %+v (%v)", quote, err)
	}

	if err = checkout.DismissOffer(ctx, stranger, offerID); !errors.Is(err, accountcheckout.ErrOfferNotFound) {
		t.Fatalf("a stranger dismissed the offer: %v", err)
	}
	if err = checkout.DismissOffer(ctx, customer, offerID); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if quote, err := store.PreviewOrder(ctx, input); err != nil || quote.DiscountMinor != 0 {
		t.Fatalf("a dismissed offer still discounts checkout: %+v (%v)", quote, err)
	}
	if left, err := checkout.Offers(ctx, customer, "en"); err != nil || len(left) != 0 {
		t.Fatalf("a dismissed offer is still listed: %+v (%v)", left, err)
	}
}
