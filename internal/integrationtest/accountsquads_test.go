//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// requireSquads turns a seeded plan version into one the customer must choose
// servers for, offering three and requiring two.
func (harness *harness) requireSquads(ctx context.Context, t *testing.T, planVersionID string) []string {
	t.Helper()
	if _, err := harness.pool.Exec(ctx,
		`UPDATE plan_versions SET squad_selection = 'required', min_selectable_squads = 2, max_selectable_squads = 2
		 WHERE id = $1::uuid`, planVersionID); err != nil {
		t.Fatalf("require squads: %v", err)
	}
	squads := []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
	}
	for index, squad := range squads {
		if _, err := harness.pool.Exec(ctx,
			`INSERT INTO plan_version_squads (plan_version_id, squad_id, label_ru, label_en, sort_order)
			 VALUES ($1::uuid, $2::uuid, 'Сервер', 'Server', $3)`, planVersionID, squad, index); err != nil {
			t.Fatalf("offer squad: %v", err)
		}
	}
	return squads
}

// A plan that asks the customer to choose servers could not be bought from the
// web at all: Open committed the session and then quoted with no selection,
// which failed, and every later read failed the same way. The checkout must
// open with the configurator and no price, accept the choice, and only then
// price and confirm.
func TestARequiredSquadPlanOpensWithoutAQuoteAndPricesOnceChosen(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	service := newAccountCheckout(t, harness, store)
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "squad-plan", 34900)
	squads := harness.requireSquads(ctx, t, planVersionID)

	view, err := service.Open(ctx, customerID, "en", accountcheckout.OpenRequest{
		PlanVersionID: planVersionID, Operation: "purchase",
	})
	if err != nil {
		t.Fatalf("open a required-squad checkout: %v", err)
	}
	if !view.SquadSelection.Required || view.SquadSelection.Reason != commerce.SquadSelectionRequired {
		t.Fatalf("squad selection = %+v, want required/squad_selection_required", view.SquadSelection)
	}
	if view.Quote.Subtotal.Amount != 0 || view.Quote.ExternalMinor != 0 {
		t.Fatalf("an unresolved checkout carried a price: %+v", view.Quote)
	}
	if !view.Squads.Configurable() || len(view.Squads.Offered) != 3 {
		t.Fatalf("the configurator was not offered: %+v", view.Squads)
	}

	// Every read agrees, rather than failing.
	if view, err = service.View(ctx, customerID, "en"); err != nil {
		t.Fatalf("read the unresolved checkout: %v", err)
	}
	if !view.SquadSelection.Required {
		t.Fatal("a re-read lost the unresolved state")
	}

	// One of two is stored and reported as too few, not refused: the screen
	// sends the whole set on every tap and has to be able to get to two.
	one := squads[:1]
	if view, err = service.Update(ctx, customerID, "en", accountcheckout.UpdateRequest{SquadIDs: &one}); err != nil {
		t.Fatalf("store a partial selection: %v", err)
	}
	if !view.SquadSelection.Required || view.SquadSelection.Reason != commerce.SquadSelectionTooFew {
		t.Fatalf("partial selection state = %+v, want too few", view.SquadSelection)
	}

	// A set the plan could never accept is refused before it is stored.
	three := squads
	if _, err = service.Update(ctx, customerID, "en", accountcheckout.UpdateRequest{SquadIDs: &three}); !errors.Is(err, commerce.ErrSquadSelection) {
		t.Fatalf("three of a maximum of two was accepted: %v", err)
	}
	unknown := []string{squads[0], "99999999-9999-4999-8999-999999999999"}
	if _, err = service.Update(ctx, customerID, "en", accountcheckout.UpdateRequest{SquadIDs: &unknown}); !errors.Is(err, commerce.ErrSquadSelection) {
		t.Fatalf("a server the plan does not offer was accepted: %v", err)
	}
	session, found, err := service.Store().Checkout(ctx, customerID)
	if err != nil || !found {
		t.Fatalf("read session: %v (found=%v)", err, found)
	}
	if len(session.SelectedSquadIDs) != 1 || session.SelectedSquadIDs[0] != squads[0] {
		t.Fatalf("a refused edit changed the stored selection to %v", session.SelectedSquadIDs)
	}

	// Confirming an unresolved checkout is refused as the squad reason, and
	// creates nothing.
	if _, err = service.ConfirmCheckout(ctx, customerID, "en"); !errors.Is(err, commerce.ErrSquadSelection) {
		t.Fatalf("an unresolved checkout was confirmed: %v", err)
	}
	var orders int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE user_id = $1::uuid`, customerID).Scan(&orders); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if orders != 0 {
		t.Fatalf("%d orders were created from an unresolved checkout", orders)
	}

	// Two of two resolves, prices, and confirms.
	two := squads[:2]
	if view, err = service.Update(ctx, customerID, "en", accountcheckout.UpdateRequest{SquadIDs: &two}); err != nil {
		t.Fatalf("store the full selection: %v", err)
	}
	if view.SquadSelection.Required {
		t.Fatalf("a complete selection is still reported as required: %+v", view.SquadSelection)
	}
	if view.Quote.Subtotal.Amount != 34900 {
		t.Fatalf("the quote after selection = %+v, want 34900", view.Quote)
	}
	order, err := service.ConfirmCheckout(ctx, customerID, "en")
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if order.ID == "" {
		t.Fatal("confirmation produced no order")
	}
}
