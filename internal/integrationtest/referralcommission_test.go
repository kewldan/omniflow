//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/referralplus"
)

func commissionTotal(ctx context.Context, t *testing.T, harness *harness, inviter string) (int64, int) {
	t.Helper()
	var total int64
	var count int
	if err := harness.pool.QueryRow(ctx, `SELECT COALESCE(sum(amount_minor), 0)::bigint, count(*)::int FROM referral_rewards
		WHERE beneficiary_user_id = $1::uuid AND role = 'commission'`, inviter).Scan(&total, &count); err != nil {
		t.Fatal(err)
	}
	return total, count
}

// The inviter earns a share of every payment their friend makes: the first
// payment at its own rate, later ones at the programme rate, top-ups when the
// programme counts them, up to the payment cap; a replayed webhook pays
// nothing twice, and a personal rate overrides the programme's.
func TestReferralCommissionOnEveryPayment(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	panel := newOperations(t, harness)
	actor := harness.operator(ctx, t, "commission@omniflow.test")
	first, cap := int32(2000), int32(3)
	if _, err := panel.SaveReferralProgram(ctx, panelpg.ReferralProgram{
		Enabled: true, Currency: "RUB", Qualification: "first_paid_order", AttributionValidityDays: 90,
		CommissionBPS: 1000, FirstPaymentCommissionBPS: &first, CommissionPaymentCap: &cap,
		CommissionTriggers: []string{"subscription", "topup"},
	}, actor); err != nil {
		t.Fatalf("save programme: %v", err)
	}
	inviter := harness.customer(ctx, t)
	friend := harness.customer(ctx, t)
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_attributions (referred_user_id, referrer_user_id, code)
		VALUES ($1::uuid, $2::uuid, 'COMMISSION')`, friend, inviter); err != nil {
		t.Fatal(err)
	}
	versionID := harness.catalog(ctx, t, "commission-plan", 10000)

	buy := func(key string, operation string) {
		order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
			CustomerID: friend, PlanVersionID: versionID, Currency: "RUB", Operation: operation,
			IdempotencyKey: key, SkipWallet: true,
		})
		if err != nil {
			t.Fatalf("order %s: %v", key, err)
		}
		harness.settle(ctx, t, store, uuidText(order.ID), 10000, key+"-event")
		harness.settle(ctx, t, store, uuidText(order.ID), 10000, key+"-event")
	}
	buy("commission-1", "purchase")
	if total, count := commissionTotal(ctx, t, harness, inviter); total != 2000 || count != 1 {
		t.Fatalf("after the first payment: %d in %d, want 2000 once", total, count)
	}
	buy("commission-2", "extension")
	topUp, err := store.CreateTopUpOrder(ctx, commercepg.TopUpInput{CustomerID: friend, Currency: "RUB", AmountMinor: 5000, IdempotencyKey: "commission-topup"})
	if err != nil {
		t.Fatal(err)
	}
	harness.settle(ctx, t, store, uuidText(topUp.ID), 5000, "commission-topup-event")
	if total, count := commissionTotal(ctx, t, harness, inviter); total != 3500 || count != 3 {
		t.Fatalf("after three payments: %d in %d, want 2000+1000+500", total, count)
	}
	buy("commission-3", "extension")
	if _, count := commissionTotal(ctx, t, harness, inviter); count != 3 {
		t.Fatalf("a payment past the cap earned: %d commissions", count)
	}
	if balance := harness.walletBalance(ctx, t, inviter); balance < 3500 {
		t.Fatalf("inviter wallet %d does not hold the commissions", balance)
	}

	// A personal rate for another inviter overrides the programme.
	partner := harness.customer(ctx, t)
	other := harness.customer(ctx, t)
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_attributions (referred_user_id, referrer_user_id, code)
		VALUES ($1::uuid, $2::uuid, 'PARTNER')`, other, partner); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_inviter_terms (user_id, commission_bps, first_payment_commission_bps)
		VALUES ($1::uuid, 3000, 3000)`, partner); err != nil {
		t.Fatal(err)
	}
	order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: other, PlanVersionID: versionID, Currency: "RUB", Operation: "purchase", IdempotencyKey: "partner-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	harness.settle(ctx, t, store, uuidText(order.ID), 10000, "partner-event")
	if total, _ := commissionTotal(ctx, t, harness, partner); total != 3000 {
		t.Fatalf("partner commission %d, want 30%%", total)
	}
}

// A commission is a money reward like the one-off inviter reward, so the
// inviter's own inviter gets the second-level share of it, once.
func TestSecondLevelShareOfCommission(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := referralplus.New(harness.pool)
	actor := harness.operator(ctx, t, "commission-levels@omniflow.test")
	grandparent := harness.customer(ctx, t)
	parent := harness.customer(ctx, t)
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_attributions (referred_user_id, referrer_user_id, code, qualified_at)
		VALUES ($1::uuid, $2::uuid, 'GRAND', now())`, parent, grandparent); err != nil {
		t.Fatal(err)
	}
	if err := service.SaveLevelSettings(ctx, referralplus.LevelSettings{Enabled: true, Percent: 10}, actor); err != nil {
		t.Fatal(err)
	}
	child := harness.seedReferralEarnings(ctx, t, parent, 5000)
	if _, err := harness.pool.Exec(ctx, `UPDATE referral_rewards SET role = 'commission'
		WHERE referred_user_id = $1::uuid`, child); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := service.Pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if balance := harness.walletBalance(ctx, t, grandparent); balance != 500 {
		t.Fatalf("second-level share of a commission = %d, want 500 once", balance)
	}
}
