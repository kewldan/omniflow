//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/referralplus"
)

// A customer applies once; a second application while one waits is refused;
// approval writes the personal rate the commission path reads, in the same
// transaction, and cannot be repeated; a declined customer may apply again.
func TestPartnerApplicationsSetThePersonalRate(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := referralplus.New(harness.pool)
	actor := harness.operator(ctx, t, "partners@omniflow.test")

	blogger := harness.customer(ctx, t)
	if _, err := service.ApplyPartner(ctx, blogger, "fax", "", ""); !errors.Is(err, referralplus.ErrInvalid) {
		t.Fatalf("an unknown source: %v", err)
	}
	application, err := service.ApplyPartner(ctx, blogger, referralplus.SourceWeb, "t.me/vpn_reviews", "20k subscribers")
	if err != nil || application.Status != "pending" {
		t.Fatalf("apply = %+v (%v)", application, err)
	}
	if _, err := service.ApplyPartner(ctx, blogger, referralplus.SourceBot, "", ""); !errors.Is(err, referralplus.ErrPartnerPending) {
		t.Fatalf("a second open application: %v", err)
	}
	listed, err := service.PartnerApplications(ctx, "pending", 10)
	if err != nil || len(listed) != 1 || listed[0].Channel != "t.me/vpn_reviews" {
		t.Fatalf("pending list = %+v (%v)", listed, err)
	}

	if _, err := service.DecidePartner(ctx, application.ID, referralplus.PartnerDecision{Approve: true}, actor); !errors.Is(err, referralplus.ErrInvalid) {
		t.Fatalf("an approval without a rate: %v", err)
	}
	rate, first := int32(2500), int32(4000)
	decided, err := service.DecidePartner(ctx, application.ID, referralplus.PartnerDecision{
		Approve: true, CommissionBPS: &rate, FirstPaymentCommissionBPS: &first, Note: "big channel",
	}, actor)
	if err != nil || decided.Status != "approved" {
		t.Fatalf("approve = %+v (%v)", decided, err)
	}
	var termsRate, termsFirst int32
	if err := harness.pool.QueryRow(ctx, `SELECT commission_bps, first_payment_commission_bps
		FROM referral_inviter_terms WHERE user_id = $1::uuid`, blogger).Scan(&termsRate, &termsFirst); err != nil ||
		termsRate != 2500 || termsFirst != 4000 {
		t.Fatalf("personal terms = %d/%d (%v)", termsRate, termsFirst, err)
	}
	if _, err := service.DecidePartner(ctx, application.ID, referralplus.PartnerDecision{Approve: false}, actor); !errors.Is(err, referralplus.ErrApplicationDecided) {
		t.Fatalf("deciding twice: %v", err)
	}
	state, err := service.PartnerStatus(ctx, blogger)
	if err != nil || !state.Partner || state.CanApply || *state.CommissionBPS != 2500 {
		t.Fatalf("partner state = %+v (%v)", state, err)
	}
	if _, err := service.ApplyPartner(ctx, blogger, referralplus.SourceBot, "", ""); !errors.Is(err, referralplus.ErrAlreadyPartner) {
		t.Fatalf("a partner applying again: %v", err)
	}
	var audits int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = 'panel.referral.partner_approved'`).
		Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("approval audits = %d (%v)", audits, err)
	}

	declined := harness.customer(ctx, t)
	second, err := service.ApplyPartner(ctx, declined, referralplus.SourceBot, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DecidePartner(ctx, second.ID, referralplus.PartnerDecision{Approve: false, Note: "no audience"}, actor); err != nil {
		t.Fatal(err)
	}
	state, err = service.PartnerStatus(ctx, declined)
	if err != nil || state.Partner || !state.CanApply {
		t.Fatalf("declined state = %+v (%v)", state, err)
	}
	var declinedTerms int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM referral_inviter_terms WHERE user_id = $1::uuid`, declined).
		Scan(&declinedTerms); err != nil || declinedTerms != 0 {
		t.Fatalf("a decline wrote terms: %d (%v)", declinedTerms, err)
	}
}

// The network shows the inviter above and two levels below, with what each
// member earned the customer, and refuses an unknown customer.
func TestReferralNetworkLevels(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service := referralplus.New(harness.pool)

	root := harness.customer(ctx, t)
	upline := harness.customer(ctx, t)
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_attributions (referred_user_id, referrer_user_id, code)
		VALUES ($1::uuid, $2::uuid, 'UP')`, root, upline); err != nil {
		t.Fatal(err)
	}
	child := harness.seedReferralEarnings(ctx, t, root, 5000)
	grandchild := harness.customer(ctx, t)
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_attributions (referred_user_id, referrer_user_id, code)
		VALUES ($1::uuid, $2::uuid, 'CHILD')`, grandchild, child); err != nil {
		t.Fatal(err)
	}

	network, err := service.ReferralNetwork(ctx, root, 50)
	if err != nil {
		t.Fatal(err)
	}
	if network.InvitedBy != upline || len(network.Levels) != 2 {
		t.Fatalf("network = %+v", network)
	}
	first, second := network.Levels[0], network.Levels[1]
	if first.Count != 1 || first.Qualified != 1 || first.EarnedMinor != 5000 || first.Members[0].CustomerID != child {
		t.Fatalf("level 1 = %+v", first)
	}
	if second.Count != 1 || second.Members[0].CustomerID != grandchild || second.Members[0].Via != child {
		t.Fatalf("level 2 = %+v", second)
	}
	if _, err := service.ReferralNetwork(ctx, "00000000-0000-0000-0000-000000000000", 10); !errors.Is(err, referralplus.ErrApplicationNotFound) {
		t.Fatalf("an unknown customer: %v", err)
	}
}
