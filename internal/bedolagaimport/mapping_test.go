package bedolagaimport

import (
	"os"
	"strings"
	"testing"
	"time"
)

const (
	versionMonth   = "11111111-1111-4111-8111-111111111111"
	versionYear    = "22222222-2222-4222-8222-222222222222"
	versionTrial   = "33333333-3333-4333-8333-333333333333"
	versionClassic = "44444444-4444-4444-8444-444444444444"
)

func int64p(value int64) *int64 { return &value }
func intp(value int) *int       { return &value }

func TestParseMappingAcceptsYAMLAndJSON(t *testing.T) {
	yamlMapping, err := ParseMapping(strings.NewReader(`
currency: rub
blockedUsers: Suspend
plans:
  - tariffId: 3
    planVersionId: ` + versionMonth + `
  - tariffId: 3
    periodDays: 360
    planVersionId: ` + versionYear + `
  - trial: true
    planVersionId: ` + versionTrial + `
`))
	if err != nil {
		t.Fatalf("parse YAML: %v", err)
	}
	if yamlMapping.Currency != "RUB" || yamlMapping.BlockedUsers != BlockedSuspend || len(yamlMapping.Plans) != 3 {
		t.Fatalf("unexpected mapping: %+v", yamlMapping)
	}
	if yamlMapping.WalletReason != DefaultWalletReason {
		t.Fatalf("wallet reason defaulted to %q", yamlMapping.WalletReason)
	}
	jsonMapping, err := ParseMapping(strings.NewReader(`{"plans":[{"noTariff":true,"planVersionId":"` + versionClassic + `"}]}`))
	if err != nil {
		t.Fatalf("parse JSON: %v", err)
	}
	if jsonMapping.Currency != "RUB" || !jsonMapping.Plans[0].NoTariff {
		t.Fatalf("unexpected mapping: %+v", jsonMapping)
	}
	empty, err := ParseMapping(strings.NewReader(""))
	if err != nil || empty.Currency != "RUB" {
		t.Fatalf("an empty mapping must parse with defaults: %+v %v", empty, err)
	}
}

func TestParseMappingRefusesAmbiguity(t *testing.T) {
	for name, body := range map[string]string{
		"unknown key":          `plans: [{tariffId: 1, planVersionID: "` + versionMonth + `"}]`,
		"bad uuid":             `plans: [{tariffId: 1, planVersionId: "not-a-uuid"}]`,
		"no selector":          `plans: [{planVersionId: "` + versionMonth + `"}]`,
		"tariff and none":      `plans: [{tariffId: 1, noTariff: true, planVersionId: "` + versionMonth + `"}]`,
		"duplicate selector":   `plans: [{tariffId: 1, planVersionId: "` + versionMonth + `"}, {tariffId: 1, planVersionId: "` + versionYear + `"}]`,
		"bad blocked policy":   `blockedUsers: ban`,
		"bad currency":         `currency: rubles`,
		"zero period":          `plans: [{tariffId: 1, periodDays: 0, planVersionId: "` + versionMonth + `"}]`,
		"negative tariff":      `plans: [{tariffId: -1, planVersionId: "` + versionMonth + `"}]`,
		"overlong reason":      `walletReason: "` + strings.Repeat("x", 201) + `"`,
		"not a mapping at all": `- 1`,
	} {
		if _, err := ParseMapping(strings.NewReader(body)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestResolvePrefersTheMostSpecificRule(t *testing.T) {
	mapping := Mapping{Plans: []PlanRule{
		{TariffID: int64p(3), PlanVersionID: versionMonth},
		{TariffID: int64p(3), PeriodDays: intp(360), PlanVersionID: versionYear},
		{Trial: true, PlanVersionID: versionTrial},
		{NoTariff: true, PlanVersionID: versionClassic},
	}}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name         string
		subscription Subscription
		want         string
	}{
		{"tariff month", Subscription{TariffID: int64p(3), StartDate: start, EndDate: start.AddDate(0, 0, 30)}, versionMonth},
		{"tariff year by period", Subscription{TariffID: int64p(3), StartDate: start, EndDate: start.AddDate(0, 0, 360)}, versionYear},
		{"trial on a tariff", Subscription{TariffID: int64p(3), IsTrial: true, StartDate: start, EndDate: start.AddDate(0, 0, 3)}, versionTrial},
		{"classic sale", Subscription{StartDate: start, EndDate: start.AddDate(0, 0, 30)}, versionClassic},
		{"unknown tariff", Subscription{TariffID: int64p(9), StartDate: start, EndDate: start.AddDate(0, 0, 30)}, ""},
	}
	for _, testCase := range cases {
		if got := mapping.Resolve(testCase.subscription); got != testCase.want {
			t.Errorf("%s: resolved %q, want %q", testCase.name, got, testCase.want)
		}
	}
	withoutTrial := Mapping{Plans: []PlanRule{{TariffID: int64p(3), PlanVersionID: versionMonth}}}
	if got := withoutTrial.Resolve(Subscription{TariffID: int64p(3), IsTrial: true, StartDate: start, EndDate: start.AddDate(0, 0, 3)}); got != versionMonth {
		t.Fatalf("a trial with no trial rule must fall back to its tariff, got %q", got)
	}
}

// The example the runbook tells operators to copy must itself be a valid
// mapping, new keys included.
func TestExampleMappingParses(t *testing.T) {
	file, err := os.Open("../../deploy/migration/mapping.example.yaml")
	if err != nil {
		t.Fatalf("open the example: %v", err)
	}
	defer func() { _ = file.Close() }()
	mapping, err := ParseMapping(file)
	if err != nil {
		t.Fatalf("the example mapping does not parse: %v", err)
	}
	if !mapping.PaymentMethodExcluded("manual") || mapping.PaymentMethodExcluded("yookassa") {
		t.Fatalf("example excludedPaymentMethods: %v", mapping.ExcludedPaymentMethods)
	}
}
