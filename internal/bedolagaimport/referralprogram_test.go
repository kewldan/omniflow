package bedolagaimport

import (
	"slices"
	"strings"
	"testing"
)

func referralPlan(t *testing.T, terms ReferralTerms, target Target, mapping Mapping) *ReferralProgramDecision {
	t.Helper()
	source, remote := fixtureSource()
	source.ReferralTerms = terms
	return BuildPlan(source, remote, target, mapping, planNow).ReferralProgram
}

// The terms a live installation ran: the levels scheme with one level that
// gives the inviter fourteen days and the friend seven on the first top-up.
// The 15% commission saved beside it belongs to the legacy scheme and pays
// nothing under levels.
func TestReferralLevelsBecomeDayRewards(t *testing.T) {
	decision := referralPlan(t, ReferralTerms{
		Read:     true,
		Settings: map[string]string{"REFERRAL_COMMISSION_PERCENT": "15", "REFERRAL_REWARD_SCHEME": "levels"},
		Levels: []ReferralLevel{{
			Level: 1, Active: true, Mode: "days", Trigger: "first_topup", ReferrerDays: 14, RefereeDays: 7,
		}},
	}, fixtureTarget(), fixtureMapping())
	if decision == nil || decision.Action != ActionCreate || decision.Scheme != "levels" {
		t.Fatalf("decision %+v", decision)
	}
	terms := decision.Terms
	if !terms.Enabled || terms.InviterRewardDays != 14 || terms.InviteeRewardDays != 7 ||
		terms.InviterRewardMinor != 0 || terms.InviteeRewardMinor != 0 || terms.CommissionBPS != 0 ||
		terms.FirstPaymentCommissionBPS != nil {
		t.Fatalf("terms %+v", terms)
	}
	if !slices.Equal(decision.Warnings, []string{WarnReferralFirstPaidOrder}) {
		t.Fatalf("warnings %v", decision.Warnings)
	}
}

// Without overrides Bedolaga paid its compiled-in legacy terms.
func TestReferralDefaultsAreTheLegacyScheme(t *testing.T) {
	decision := referralPlan(t, ReferralTerms{Read: true}, fixtureTarget(), fixtureMapping())
	terms := decision.Terms
	if decision.Action != ActionCreate || decision.Scheme != "legacy" || terms.InviterRewardMinor != 10000 ||
		terms.InviteeRewardMinor != 10000 || terms.CommissionBPS != 2500 || terms.CommissionPaymentCap != nil ||
		!slices.Equal(terms.CommissionTriggers, []string{"subscription", "topup"}) {
		t.Fatalf("decision %+v", decision)
	}
	if !slices.Contains(decision.Warnings, WarnReferralMinimumTopup) {
		t.Fatalf("warnings %v", decision.Warnings)
	}
}

// A key in Bedolaga's .env wins over the one saved from its admin panel, as
// it did in the bot; the ladder's zero rung is the base rate.
func TestReferralEnvWinsOverTheDatabase(t *testing.T) {
	mapping := fixtureMapping()
	mapping.ReferralSettings = map[string]string{
		"REFERRAL_COMMISSION_PERCENT": "20", "REFERRAL_RECURRING_COMMISSION_TIERS": "0:10, 10:15,bad,50:20",
		"REFERRAL_MAX_COMMISSION_PAYMENTS": "3", "REFERRAL_FIRST_PAYMENT_COMMISSION_PERCENT": "30",
	}
	decision := referralPlan(t, ReferralTerms{
		Read: true, Settings: map[string]string{"REFERRAL_COMMISSION_PERCENT": "15"},
	}, fixtureTarget(), mapping)
	terms := decision.Terms
	if terms.CommissionBPS != 1000 || *terms.FirstPaymentCommissionBPS != 3000 || *terms.CommissionPaymentCap != 3 ||
		!slices.Equal(terms.CommissionTiers, []ReferralCommission{{Referrals: 10, BPS: 1500}, {Referrals: 50, BPS: 2000}}) {
		t.Fatalf("terms %+v", terms)
	}
}

// An operator's programme stands unless it already pays exactly this.
func TestReferralProgramAlreadySaved(t *testing.T) {
	terms := ReferralTerms{Read: true, Settings: map[string]string{"REFERRAL_REWARD_SCHEME": "levels"},
		Levels: []ReferralLevel{{Level: 1, Active: true, Mode: "days", Trigger: "first_topup", ReferrerDays: 14, RefereeDays: 7}}}
	first := referralPlan(t, terms, fixtureTarget(), fixtureMapping())

	target := fixtureTarget()
	same := first.Terms
	same.CommissionTriggers = []string{"topup", "subscription"}
	target.ReferralProgram = &same
	if decision := referralPlan(t, terms, target, fixtureMapping()); decision.Action != ActionUnchanged {
		t.Fatalf("same programme %+v", decision)
	}
	other := first.Terms
	other.InviterRewardDays = 30
	target.ReferralProgram = &other
	decision := referralPlan(t, terms, target, fixtureMapping())
	if decision.Action != ActionSkip || !slices.Equal(decision.Codes, []string{CodeReferralProgramExists}) {
		t.Fatalf("operator's programme %+v", decision)
	}
}

// Levels deeper than one, a referral threshold, tariff rewards, and fixed sums
// on every top-up are reported, not carried; a percent on every top-up is the
// commission.
func TestReferralLevelsThatOmniflowCannotSay(t *testing.T) {
	percent, fixed := int64(12), int64(5000)
	decision := referralPlan(t, ReferralTerms{Read: true, Settings: map[string]string{"REFERRAL_REWARD_SCHEME": "levels"},
		Levels: []ReferralLevel{
			{Level: 1, Active: true, Mode: "both", Trigger: "every_topup", ReferrerPercent: &percent,
				ReferrerFixedKopeks: &fixed, MaxPayments: 5, RequiredReferrals: 2, TariffReward: true},
			{Level: 2, Active: true, Mode: "money", Trigger: "every_topup", ReferrerPercent: &percent},
		}}, fixtureTarget(), fixtureMapping())
	terms := decision.Terms
	if decision.Action != ActionCreate || terms.CommissionBPS != 1200 || *terms.CommissionPaymentCap != 5 ||
		terms.InviterRewardMinor != 0 || terms.InviterRewardDays != 0 {
		t.Fatalf("decision %+v", decision)
	}
	for _, code := range []string{WarnReferralDeeperLevels, WarnReferralRequiredReferrals, WarnReferralTariffReward, WarnReferralRecurringReward} {
		if !slices.Contains(decision.Warnings, code) {
			t.Fatalf("missing %s in %v", code, decision.Warnings)
		}
	}
}

// A programme Bedolaga had switched off is carried switched off; one that
// rewards nobody is not carried at; a database without system_settings
// decides nothing.
func TestReferralProgramOffOrEmpty(t *testing.T) {
	off := referralPlan(t, ReferralTerms{Read: true, Settings: map[string]string{"REFERRAL_PROGRAM_ENABLED": "false"}},
		fixtureTarget(), fixtureMapping())
	if off.Action != ActionCreate || off.Terms.Enabled || !slices.Contains(off.Warnings, WarnReferralDisabled) {
		t.Fatalf("switched off %+v", off)
	}
	empty := referralPlan(t, ReferralTerms{Read: true, Settings: map[string]string{"REFERRAL_REWARD_SCHEME": "levels"}},
		fixtureTarget(), fixtureMapping())
	if empty.Action != ActionSkip || !slices.Equal(empty.Codes, []string{CodeReferralProgramPaysNothing}) {
		t.Fatalf("pays nothing %+v", empty)
	}
	if decision := referralPlan(t, ReferralTerms{}, fixtureTarget(), fixtureMapping()); decision != nil {
		t.Fatalf("unread %+v", decision)
	}
}

func TestMappingReferralSettingsAreReferralKeysOnly(t *testing.T) {
	if _, err := ParseMapping(strings.NewReader("referralSettings:\n  BOT_TOKEN: x\n")); err == nil {
		t.Fatal("a non-referral key was accepted")
	}
	mapping, err := ParseMapping(strings.NewReader("referralSettings:\n  referral_commission_percent: ' 20 '\n"))
	if err != nil || mapping.ReferralSettings["REFERRAL_COMMISSION_PERCENT"] != "20" {
		t.Fatalf("mapping %+v, %v", mapping.ReferralSettings, err)
	}
}
