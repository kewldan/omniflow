//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/accountpg"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// newAccountCheckout wires the shared customer checkout against the migrated
// database.
//
// The payment service is deliberately nil. Everything these tests are about —
// what an order costs, which one it becomes, and whose it is — is settled before
// a provider is ever asked for money, and a checkout that needed a live adapter
// to answer those questions would be answering them in the wrong place.
func newAccountCheckout(t *testing.T, harness *harness, store *commercepg.Store) *accountcheckout.Service {
	t.Helper()
	service, err := accountcheckout.New(harness.pool, store, nil, accountcheckout.Options{
		Logger: slog.New(slog.DiscardHandler),
		Settings: accountcheckout.Settings{
			Currency: "RUB", PublicURL: "https://example.test", TermsURL: "https://example.test/terms",
			MultiSubscription: true,
		},
	})
	if err != nil {
		t.Fatalf("build checkout: %v", err)
	}
	return service
}

// seedWallet credits a balance directly, which is what a settled top-up or a
// referral reward eventually leaves behind.
func (harness *harness) seedWallet(
	ctx context.Context, t *testing.T, customerID, key string, amountMinor int64,
) {
	t.Helper()
	if _, err := harness.pool.Exec(ctx, `WITH t AS (
			INSERT INTO ledger_transactions (type, reference_type, reference_id, idempotency_key)
			VALUES ('credit', 'test', $2, $2) RETURNING id)
		INSERT INTO ledger_entries (transaction_id, account_type, user_id, currency, amount_minor)
		SELECT id, 'customer_wallet', $1::uuid, 'RUB', $3::bigint FROM t
		UNION ALL SELECT id, 'platform_clearing', NULL, 'RUB', 0 - $3::bigint FROM t`,
		customerID, key, amountMinor); err != nil {
		t.Fatalf("seed wallet: %v", err)
	}
}

// promotion publishes one campaign and a code that redeems it, optionally
// scoping it to a plan. A promotion with no plan link is one no plan can use,
// which is exactly the shape an ineligible code has.
func (harness *harness) promotion(
	ctx context.Context, t *testing.T, code, planID string, perCustomerLimit int,
) {
	t.Helper()
	var promotionID string
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO promotions (code, kind, value, per_customer_limit)
		 VALUES ($1, 'percent', 1000, $2) RETURNING id::text`,
		strings.ToLower(code), perCustomerLimit).Scan(&promotionID); err != nil {
		t.Fatalf("create promotion: %v", err)
	}
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO promo_codes (promotion_id, normalized_code) VALUES ($1::uuid, $2)`,
		promotionID, strings.ToUpper(code)); err != nil {
		t.Fatalf("create promo code: %v", err)
	}
	if planID == "" {
		return
	}
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO promotion_plans (promotion_id, plan_id) VALUES ($1::uuid, $2::uuid)`,
		promotionID, planID); err != nil {
		t.Fatalf("scope promotion: %v", err)
	}
}

// planIDOf recovers the plan a version belongs to, which the promotion scoping
// is keyed on.
func (harness *harness) planIDOf(ctx context.Context, t *testing.T, planVersionID string) string {
	t.Helper()
	var planID string
	if err := harness.pool.QueryRow(ctx,
		`SELECT plan_id::text FROM plan_versions WHERE id = $1::uuid`, planVersionID).Scan(&planID); err != nil {
		t.Fatalf("read plan: %v", err)
	}
	return planID
}

// openSession starts a checkout and hands back the stored session, which is
// what the confirmation path actually acts on.
func openSession(
	ctx context.Context, t *testing.T, service *accountcheckout.Service, customerID, planVersionID, operation string,
) accountcheckout.Session {
	t.Helper()
	if _, err := service.Open(ctx, customerID, "en", accountcheckout.OpenRequest{
		PlanVersionID: planVersionID, Operation: operation,
	}); err != nil {
		t.Fatalf("open checkout: %v", err)
	}
	session, found, err := service.Store().Checkout(ctx, customerID)
	if err != nil || !found {
		t.Fatalf("read checkout: %v (found=%v)", err, found)
	}
	return session
}

// TestConfirmingACheckoutTwiceProducesOneOrder is the invariant a purchase
// screen lives or dies by. A second tap, a resubmitted form, and a retry after a
// dropped connection all arrive as a second confirmation of the same checkout,
// and none of them may take the customer's money twice.
func TestConfirmingACheckoutTwiceProducesOneOrder(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	service := newAccountCheckout(t, harness, store)
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "duplicate-plan", 34900)

	session := openSession(ctx, t, service, customerID, planVersionID, "purchase")
	first, err := service.Confirm(ctx, session)
	if err != nil {
		t.Fatalf("first confirmation: %v", err)
	}
	second, err := service.Confirm(ctx, session)
	if err != nil {
		t.Fatalf("second confirmation: %v", err)
	}
	if first != second {
		t.Fatalf("a duplicate confirmation created a second order: %s and %s", first, second)
	}
	var orders int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM orders WHERE user_id = $1::uuid`, customerID).Scan(&orders); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if orders != 1 {
		t.Fatalf("expected exactly one order, got %d", orders)
	}
	// The checkout is bound to the order it became, so the customer is not left
	// holding a session that would open a second purchase of the same thing.
	if _, found, checkErr := service.Store().Checkout(ctx, customerID); checkErr != nil || found {
		t.Fatalf("a confirmed checkout is still open (found=%v, err=%v)", found, checkErr)
	}
}

// TestEveryPromoRefusalCarriesItsOwnReason proves the four rejections are
// distinguishable. A panel that could only say "that code did not work" would
// send a customer with an exhausted code hunting for a typo.
func TestEveryPromoRefusalCarriesItsOwnReason(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	service := newAccountCheckout(t, harness, store)
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "promo-plan", 34900)
	planID := harness.planIDOf(ctx, t, planVersionID)

	// One campaign scoped to a different plan, and one scoped to this one. A
	// campaign with no plan links at all would apply to everything, which is the
	// opposite of what an ineligible code looks like.
	otherPlanID := harness.planIDOf(ctx, t, harness.catalog(ctx, t, "other-plan", 9900))
	harness.promotion(ctx, t, "elsewhere", otherPlanID, 5)
	harness.promotion(ctx, t, "spring10", planID, 1)

	openSession(ctx, t, service, customerID, planVersionID, "purchase")
	// The order matters: the accepted code is applied last so the assertions
	// after the loop read the state it left behind.
	for _, attempt := range []struct{ code, want string }{
		{"!!", accountcheckout.PromoInvalid},
		{"NOSUCHCODE", accountcheckout.PromoUnknown},
		{"ELSEWHERE", accountcheckout.PromoIneligible},
		{"  spring10  ", ""},
	} {
		view, err := service.ApplyPromoCode(ctx, customerID, "en", attempt.code)
		if err != nil {
			t.Fatalf("apply %q: %v", attempt.code, err)
		}
		if view.PromoRejection != attempt.want {
			t.Fatalf("applying %q reported %q, want %q", attempt.code, view.PromoRejection, attempt.want)
		}
	}
	// The accepted code survives into the quote rather than being silently
	// dropped, and it actually takes ten percent off.
	view, err := service.View(ctx, customerID, "en")
	if err != nil {
		t.Fatalf("read checkout: %v", err)
	}
	if view.Quote.PromoCode != "SPRING10" || view.Quote.DiscountMinor != 3490 {
		t.Fatalf("the accepted promotion produced %+v", view.Quote)
	}

	// Redeem it, then try again: one per customer means the second attempt is
	// exhausted, not unknown and not ineligible.
	session, _, err := service.Store().Checkout(ctx, customerID)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if _, err = service.Confirm(ctx, session); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	openSession(ctx, t, service, customerID, planVersionID, "purchase")
	again, err := service.ApplyPromoCode(ctx, customerID, "en", "SPRING10")
	if err != nil {
		t.Fatalf("reapply: %v", err)
	}
	if again.PromoRejection != accountcheckout.PromoExhausted {
		t.Fatalf("a spent code reported %q", again.PromoRejection)
	}
}

// TestWalletApplicationIsBrokenDownExactly checks the arithmetic the customer is
// shown before they commit. The balance, what it covers, and what is still owed
// are three separate numbers, and a screen that collapsed them could not explain
// why the card was charged at all.
func TestWalletApplicationIsBrokenDownExactly(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	service := newAccountCheckout(t, harness, store)
	customerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "wallet-plan", 34900)
	harness.seedWallet(ctx, t, customerID, "wallet-seed", 20000)

	view, err := service.Open(ctx, customerID, "en", accountcheckout.OpenRequest{
		PlanVersionID: planVersionID, Operation: "purchase",
	})
	if err != nil {
		t.Fatalf("open checkout: %v", err)
	}
	if view.Quote.Subtotal.Amount != 34900 || view.Quote.Subtotal.Currency != "RUB" {
		t.Fatalf("the subtotal was %+v", view.Quote.Subtotal)
	}
	if view.Quote.WalletBalanceMinor != 20000 || view.Quote.WalletAppliedMinor != 20000 {
		t.Fatalf("the wallet application was %+v", view.Quote)
	}
	if view.Quote.ExternalMinor != 14900 {
		t.Fatalf("the remainder was %d, want 14900", view.Quote.ExternalMinor)
	}

	// Turning the wallet off must leave the balance visible and move the whole
	// price to the provider, not hide the credit the customer still holds.
	off := false
	withoutWallet, err := service.Update(ctx, customerID, "en", accountcheckout.UpdateRequest{ApplyWallet: &off})
	if err != nil {
		t.Fatalf("disable wallet: %v", err)
	}
	if withoutWallet.Quote.WalletAppliedMinor != 0 || withoutWallet.Quote.ExternalMinor != 34900 {
		t.Fatalf("disabling the wallet produced %+v", withoutWallet.Quote)
	}
	if withoutWallet.Quote.WalletBalanceMinor != 20000 {
		t.Fatalf("the balance disappeared when it stopped being applied: %+v", withoutWallet.Quote)
	}

	// Confirming spends exactly what was shown.
	on := true
	if _, err = service.Update(ctx, customerID, "en", accountcheckout.UpdateRequest{ApplyWallet: &on}); err != nil {
		t.Fatalf("re-enable wallet: %v", err)
	}
	order, err := service.ConfirmCheckout(ctx, customerID, "en")
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if order.WalletMinor != 20000 || order.ExternalMinor != 14900 {
		t.Fatalf("the order recorded %d from the wallet and %d external",
			order.WalletMinor, order.ExternalMinor)
	}
}

// TestALifecycleFlowMustNameItsSubscription covers the targeting rule. Once an
// installation allows several subscriptions, a renewal that guessed which one it
// renewed would be wrong roughly half the time and silent about it.
func TestALifecycleFlowMustNameItsSubscription(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	service := newAccountCheckout(t, harness, store)
	customerID := harness.customer(ctx, t)
	strangerID := harness.customer(ctx, t)
	planVersionID := harness.catalog(ctx, t, "target-plan", 34900)

	var firstID, secondID string
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO subscriptions (user_id, slot, label) VALUES ($1::uuid, 1, 'Subscription 1')
		 RETURNING id::text`, customerID).Scan(&firstID); err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO subscriptions (user_id, slot, label) VALUES ($1::uuid, 2, 'Subscription 2')
		 RETURNING id::text`, customerID).Scan(&secondID); err != nil {
		t.Fatalf("create second subscription: %v", err)
	}
	var strangerSubscriptionID string
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO subscriptions (user_id, slot, label) VALUES ($1::uuid, 1, 'Subscription 1')
		 RETURNING id::text`, strangerID).Scan(&strangerSubscriptionID); err != nil {
		t.Fatalf("create stranger subscription: %v", err)
	}

	_, err := service.Open(ctx, customerID, "en", accountcheckout.OpenRequest{
		PlanVersionID: planVersionID, Operation: "renewal",
	})
	if !errors.Is(err, accountcheckout.ErrSubscriptionTargetRequired) {
		t.Fatalf("an unnamed renewal was accepted: %v", err)
	}
	// It reads as a validation failure too, so a transport that has not been
	// taught the specific state still answers 422 rather than 500.
	if !errors.Is(err, accountpg.ErrInvalidInput) {
		t.Fatalf("the refusal is not a validation failure: %v", err)
	}

	named, err := service.Open(ctx, customerID, "en", accountcheckout.OpenRequest{
		PlanVersionID: planVersionID, Operation: "renewal", SubscriptionID: secondID,
	})
	if err != nil {
		t.Fatalf("named renewal: %v", err)
	}
	if named.SubscriptionID != secondID {
		t.Fatalf("the renewal targeted %q, want %q", named.SubscriptionID, secondID)
	}
	if !named.MultiSubscription || len(named.Targets) != 2 {
		t.Fatalf("the target picker was not offered: %+v", named.Targets)
	}

	// Somebody else's subscription is not a validation error to be explained; it
	// is indistinguishable from one that does not exist.
	_, err = service.Open(ctx, customerID, "en", accountcheckout.OpenRequest{
		PlanVersionID: planVersionID, Operation: "renewal", SubscriptionID: strangerSubscriptionID,
	})
	if !errors.Is(err, accountcheckout.ErrOrderNotFound) {
		t.Fatalf("another customer's subscription was addressable: %v", err)
	}

	// Retargeting an open checkout is checked the same way.
	_, err = service.Update(ctx, customerID, "en", accountcheckout.UpdateRequest{
		SubscriptionID: &strangerSubscriptionID,
	})
	if !errors.Is(err, accountcheckout.ErrOrderNotFound) {
		t.Fatalf("a checkout could be retargeted onto another customer: %v", err)
	}
	retargeted, err := service.Update(ctx, customerID, "en", accountcheckout.UpdateRequest{
		SubscriptionID: &firstID,
	})
	if err != nil {
		t.Fatalf("retarget: %v", err)
	}
	if retargeted.SubscriptionID != firstID {
		t.Fatalf("retargeting produced %q", retargeted.SubscriptionID)
	}
}

// TestOrderHistoryIsScopedToItsOwner proves ownership is part of the query
// rather than a check somebody has to remember to write. Every read and every
// mutation is addressed by customer and identifier together.
func TestOrderHistoryIsScopedToItsOwner(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := commercepg.New(harness.pool, nil, testOptions())
	service := newAccountCheckout(t, harness, store)
	planVersionID := harness.catalog(ctx, t, "history-plan", 34900)
	ownerID := harness.customer(ctx, t)
	strangerID := harness.customer(ctx, t)

	ownerOrder, err := service.Confirm(ctx, openSession(ctx, t, service, ownerID, planVersionID, "purchase"))
	if err != nil {
		t.Fatalf("owner order: %v", err)
	}
	strangerOrder, err := service.Confirm(ctx, openSession(ctx, t, service, strangerID, planVersionID, "purchase"))
	if err != nil {
		t.Fatalf("stranger order: %v", err)
	}

	orders, err := service.Orders(ctx, ownerID, "en", accountcheckout.Cursor{}, 50)
	if err != nil {
		t.Fatalf("list orders: %v", err)
	}
	if len(orders) != 1 || orders[0].ID != ownerOrder {
		t.Fatalf("the history showed %d orders: %+v", len(orders), orders)
	}

	if _, _, err = service.Order(ctx, ownerID, strangerOrder, "en"); !errors.Is(err, accountcheckout.ErrOrderNotFound) {
		t.Fatalf("another customer's order was readable: %v", err)
	}
	// The same answer a genuinely missing identifier gets, so an order ID cannot
	// be probed for existence.
	if _, _, err = service.Order(ctx, ownerID, "00000000-0000-0000-0000-000000000000", "en"); !errors.Is(err, accountcheckout.ErrOrderNotFound) {
		t.Fatalf("a missing order reported %v", err)
	}
	if err = service.CancelOrder(ctx, ownerID, strangerOrder); !errors.Is(err, accountcheckout.ErrOrderNotFound) {
		t.Fatalf("another customer's order was cancellable: %v", err)
	}
	refunds, err := service.Store().Refunds(ctx, ownerID, strangerOrder)
	if err != nil || len(refunds) != 0 {
		t.Fatalf("another customer's refunds were readable: %v, %+v", err, refunds)
	}

	// The stranger's own order is still there and still theirs.
	if _, _, err = service.Order(ctx, strangerID, strangerOrder, "en"); err != nil {
		t.Fatalf("the owner lost access to their own order: %v", err)
	}
}
