package commerce

import (
	"testing"
	"time"
)

func TestQuoteAppliesWalletFirstAndRespectsOptOut(t *testing.T) {
	price, err := NewMoney(50000, "RUB")
	if err != nil {
		t.Fatalf("price: %v", err)
	}
	applied, err := Quote(price, 5000, 30000, true)
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if applied.WalletAppliedMinor != 30000 || applied.ExternalMinor != 15000 {
		t.Fatalf("quote = %+v, want 30000 wallet and 15000 external", applied)
	}
	optedOut, err := Quote(price, 5000, 30000, false)
	if err != nil {
		t.Fatalf("quote without wallet: %v", err)
	}
	if optedOut.WalletAppliedMinor != 0 || optedOut.ExternalMinor != 45000 || optedOut.WalletBalanceMinor != 30000 {
		t.Fatalf("quote = %+v, want the full amount payable externally", optedOut)
	}
}

func TestQuoteNeverExceedsTheDueAmount(t *testing.T) {
	price, err := NewMoney(10000, "RUB")
	if err != nil {
		t.Fatalf("price: %v", err)
	}
	quote, err := Quote(price, 0, 999999, true)
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if quote.WalletAppliedMinor != 10000 || quote.ExternalMinor != 0 {
		t.Fatalf("quote = %+v, want the wallet capped at the due amount", quote)
	}
}

func TestEligibleProvidersFiltersByEnablementAndCurrency(t *testing.T) {
	options := []PaymentOption{
		{Provider: "telegram_stars", Enabled: true, Currencies: []string{"XTR"}, Order: 1},
		{Provider: "yookassa", Enabled: true, Currencies: []string{"RUB"}, Order: 2, Recurring: true},
		{Provider: "cryptobot", Enabled: false, Currencies: []string{"RUB", "USD"}, Order: 3},
	}
	eligible := EligibleProviders(options, "RUB", 15000)
	if len(eligible) != 1 || eligible[0].Provider != "yookassa" {
		t.Fatalf("eligible = %+v, want only yookassa", eligible)
	}
	if len(EligibleProviders(options, "RUB", 0)) != 0 {
		t.Fatal("an order with nothing left to pay must offer no provider")
	}
	if !SupportsAutoRenew(options, "RUB") {
		t.Fatal("a recurring-capable enabled adapter must support auto-renew")
	}
	if SupportsAutoRenew(options, "XTR") {
		t.Fatal("auto-renew must not be advertised when no recurring adapter settles the currency")
	}
}

func TestEvaluatePaymentPhaseKeepsPaidOrdersForward(t *testing.T) {
	for name, expected := range map[string]struct {
		state       OrderState
		intent      string
		fulfillment string
		phase       PaymentPhase
	}{
		"late webhook after fulfillment": {OrderFulfilled, "pending", "succeeded", PaymentPhaseCompleted},
		"paid and provisioning":          {OrderPaid, "succeeded", "retrying", PaymentPhaseProvisioning},
		"paid and provisioning failed":   {OrderPaid, "succeeded", "failed", PaymentPhaseFailed},
		"awaiting customer action":       {OrderPending, "requires_action", "", PaymentPhaseAwaitingAction},
		"expired payment window":         {OrderExpired, "pending", "", PaymentPhaseExpired},
		"refunded":                       {OrderRefunded, "succeeded", "succeeded", PaymentPhaseRefunded},
	} {
		phase := EvaluatePaymentPhase(expected.state, expected.intent, expected.fulfillment)
		if phase != expected.phase {
			t.Fatalf("%s: phase = %q, want %q", name, phase, expected.phase)
		}
	}
}

func TestQualifyReferralGrantsExactlyOnce(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	program := ReferralProgram{Enabled: true, Currency: "RUB", InviterRewardMinor: 10000, InviteeRewardMinor: 5000, Qualification: "first_paid_order", AttributionValidity: 90 * 24 * time.Hour}
	attribution := ReferralAttribution{AttributedAt: now.Add(-24 * time.Hour), OrderState: OrderPaid, OrderPaidMinor: 50000, OrderCurrency: "RUB"}
	rewards, err := QualifyReferral(now, program, attribution)
	if err != nil {
		t.Fatalf("qualify: %v", err)
	}
	if len(rewards) != 2 {
		t.Fatalf("rewards = %+v, want an inviter and an invitee reward", rewards)
	}
	attribution.GrantedRoles = map[string]bool{"inviter": true, "invitee": true}
	replayed, err := QualifyReferral(now, program, attribution)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(replayed) != 0 {
		t.Fatalf("replayed rewards = %+v, want none", replayed)
	}
}

func TestQualifyReferralRespectsCapValidityAndSettlement(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	cap := 3
	program := ReferralProgram{Enabled: true, Currency: "RUB", InviterRewardMinor: 10000, Qualification: "first_paid_order", AttributionValidity: 30 * 24 * time.Hour, InviterRewardCap: &cap}
	capped, err := QualifyReferral(now, program, ReferralAttribution{AttributedAt: now, OrderState: OrderPaid, OrderPaidMinor: 1, InviterRewardCount: 3})
	if err != nil || len(capped) != 0 {
		t.Fatalf("capped rewards = (%+v, %v), want none", capped, err)
	}
	stale, err := QualifyReferral(now, program, ReferralAttribution{AttributedAt: now.Add(-60 * 24 * time.Hour), OrderState: OrderPaid, OrderPaidMinor: 1})
	if err != nil || len(stale) != 0 {
		t.Fatalf("expired attribution rewards = (%+v, %v), want none", stale, err)
	}
	unsettled, err := QualifyReferral(now, program, ReferralAttribution{AttributedAt: now, OrderState: OrderPaid, OrderPaidMinor: 0})
	if err != nil || len(unsettled) != 0 {
		t.Fatalf("unsettled order rewards = (%+v, %v), want none", unsettled, err)
	}
	pending, err := QualifyReferral(now, program, ReferralAttribution{AttributedAt: now, OrderState: OrderPending, OrderPaidMinor: 50000})
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending order rewards = (%+v, %v), want none", pending, err)
	}
}
