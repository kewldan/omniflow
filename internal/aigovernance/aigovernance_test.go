package aigovernance

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// spend answers what a scope has used, keyed by scope and reference.
type spend map[string]Spend

func (reader spend) Spent(
	_ context.Context, scope, ref, feature string, _ time.Duration,
) (Spend, error) {
	if scope == "broken" {
		return Spend{}, errors.New("the meter is unreadable")
	}
	return reader[scope+"/"+ref+"/"+feature], nil
}

// unreadable fails every lookup, which is the case a ceiling has to survive.
type unreadable struct{}

func (unreadable) Spent(
	context.Context, string, string, string, time.Duration,
) (Spend, error) {
	return Spend{}, errors.New("the meter is unreadable")
}

func enabled() Feature {
	return Feature{
		Name: FeatureSupportReply, Enabled: true, Provider: "acme", Model: "acme-1",
		RetainOutputs: true, RetentionDays: 30,
	}
}

// An installation should never discover its AI settings by watching a request
// fail.
func TestAFeatureWithNoConfigurationIsOff(t *testing.T) {
	policy := NewPolicy(nil, nil, spend{})
	if policy.Enabled(FeatureCopilot) {
		t.Fatal("an unconfigured feature reported as enabled")
	}
	if err := policy.Allow(
		context.Background(), FeatureCopilot, Actor{OperatorID: "op-1"},
	); !errors.Is(err, ErrFeatureDisabled) {
		t.Fatalf("expected ErrFeatureDisabled, got %v", err)
	}
}

// A switch that reads as on and behaves as off is the worst of both.
func TestEnabledWithoutAProviderIsNotUsable(t *testing.T) {
	half := Feature{Name: FeatureCopilot, Enabled: true}
	if half.Usable() {
		t.Fatal("a feature with no provider reported as usable")
	}
	policy := NewPolicy([]Feature{half}, nil, spend{})
	if policy.Enabled(FeatureCopilot) {
		t.Fatal("a misconfigured feature was offered")
	}
	// And the settings screen can still tell the two apart.
	if !policy.Feature(FeatureCopilot).Enabled {
		t.Fatal("the owner's switch was lost, so the screen cannot explain the state")
	}
}

// The remedy differs, so the message has to name the ceiling the operator can
// do something about.
func TestTheNarrowestCeilingIsTheOneReported(t *testing.T) {
	policy := NewPolicy([]Feature{enabled()}, []Limit{
		{Scope: ScopeInstallation, Window: time.Hour, MaxRequests: 100},
		{Scope: ScopeRole, Ref: "support", Window: time.Hour, MaxRequests: 50},
		{Scope: ScopeOperator, Ref: "op-1", Window: time.Hour, MaxRequests: 10},
	}, spend{
		"installation//":      {Requests: 500},
		"role/support/":       {Requests: 500},
		"operator/op-1/":      {Requests: 500},
		"feature//" + "reply": {},
	})

	err := policy.Allow(context.Background(), FeatureSupportReply,
		Actor{OperatorID: "op-1", Role: "support"})
	if !errors.Is(err, ErrLimitReached) {
		t.Fatalf("expected ErrLimitReached, got %v", err)
	}
	if !strings.Contains(err.Error(), "your own allowance") {
		t.Fatalf("the widest ceiling was reported instead of the narrowest: %v", err)
	}
}

// An operator under their own allowance is still stopped by the installation's.
func TestAWiderCeilingStillBinds(t *testing.T) {
	policy := NewPolicy([]Feature{enabled()}, []Limit{
		{Scope: ScopeOperator, Ref: "op-1", Window: time.Hour, MaxRequests: 100},
		{Scope: ScopeInstallation, Window: time.Hour, MaxCostMinor: 5000},
	}, spend{
		"operator/op-1/": {Requests: 3},
		"installation//": {CostMinor: 9000},
	})
	err := policy.Allow(context.Background(), FeatureSupportReply, Actor{OperatorID: "op-1"})
	if !errors.Is(err, ErrLimitReached) || !strings.Contains(err.Error(), "installation-wide") {
		t.Fatalf("the installation ceiling did not bind: %v", err)
	}
}

// A per-feature ceiling applies to everyone using that feature and to nobody
// using another.
func TestAFeatureCeilingIsScopedToItsFeature(t *testing.T) {
	features := []Feature{enabled(), {
		Name: FeatureMarketingDraft, Enabled: true, Provider: "acme", Model: "acme-1",
	}}
	policy := NewPolicy(features, []Limit{
		{Scope: ScopeFeature, Feature: FeatureSupportReply, Window: time.Hour, MaxTokens: 1000},
	}, spend{"feature//" + FeatureSupportReply: {Tokens: 5000}})

	if err := policy.Allow(
		context.Background(), FeatureSupportReply, Actor{OperatorID: "op-1"},
	); !errors.Is(err, ErrLimitReached) {
		t.Fatalf("the feature ceiling did not bind: %v", err)
	}
	if err := policy.Allow(
		context.Background(), FeatureMarketingDraft, Actor{OperatorID: "op-1"},
	); err != nil {
		t.Fatalf("a ceiling for one feature stopped another: %v", err)
	}
}

// Spending against a limit nobody can measure is how an installation learns its
// ceiling from an invoice.
func TestAnUnreadableMeterFailsClosed(t *testing.T) {
	policy := NewPolicy([]Feature{enabled()}, []Limit{
		{Scope: ScopeInstallation, Window: time.Hour, MaxRequests: 10},
	}, unreadable{})
	if err := policy.Allow(
		context.Background(), FeatureSupportReply, Actor{},
	); !errors.Is(err, ErrLimitReached) {
		t.Fatalf("an unreadable meter allowed the call: %v", err)
	}
}

// Refusing when no limits are configured would take every feature offline on
// the common case.
func TestNoLimitsMeansNoCeiling(t *testing.T) {
	policy := NewPolicy([]Feature{enabled()}, nil, spend{})
	if err := policy.Allow(context.Background(), FeatureSupportReply, Actor{}); err != nil {
		t.Fatalf("an installation with no limits was refused: %v", err)
	}
}

// The person reading a warning is deciding whether their customers' messages
// may leave, so the ones that matter are stated before the switch is flipped.
func TestWarningsDescribeWhatEnablingMeans(t *testing.T) {
	warnings := Warnings(
		Feature{
			Name: FeatureSupportReply, Provider: "acme", Model: "acme-1",
			RetainPrompts: true, RetentionDays: 90,
		},
		Provider{Slug: "acme", TrainsOnData: true},
	)
	codes := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		codes = append(codes, warning.Code)
	}
	for _, expected := range []string{
		WarningTrainsOnData, WarningNoZeroRetention,
		WarningRegionUnstated, WarningProviderUnchecked, WarningPromptsRetained,
	} {
		if !slices.Contains(codes, expected) {
			t.Fatalf("missing %s: %v", expected, codes)
		}
	}
	for _, warning := range warnings {
		if warning.Code == WarningPromptsRetained && !strings.Contains(warning.Text, "90") {
			t.Fatalf("the retention warning does not state the period: %q", warning.Text)
		}
	}
}

// A well-configured provider produces only a blocking-free set, and a feature
// with no provider produces a blocking one.
func TestABlockingWarningMeansItCannotBeEnabled(t *testing.T) {
	blocking := Warnings(Feature{Name: FeatureCopilot}, Provider{})
	if len(blocking) != 1 || !blocking[0].Blocking {
		t.Fatalf("a feature with no provider was not blocked: %+v", blocking)
	}

	clean := Warnings(
		Feature{Name: FeatureCopilot, Provider: "acme", Model: "acme-1"},
		Provider{
			Slug: "acme", ZeroRetention: true, DataRegion: "eu",
			LastCheckOK: true, LastCheckedAt: time.Now(),
		},
	)
	if len(clean) != 0 {
		t.Fatalf("a well-configured provider produced warnings: %+v", clean)
	}
}

// The safe default is not to keep, and "not stored" reads differently from
// "stored forever".
func TestRetentionDefaultsToNotKeepingPrompts(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	policy := NewPolicy([]Feature{enabled()}, nil, spend{})

	retention := policy.RetentionFor(FeatureSupportReply, now, false)
	if retention.KeepPrompt {
		t.Fatal("prompts were kept without the owner asking")
	}
	if !retention.KeepOutput || retention.Until.IsZero() {
		t.Fatalf("a kept output has no deletion date: %+v", retention)
	}
	if retention.Until != now.AddDate(0, 0, 30).UTC() {
		t.Fatalf("the deletion date does not match the policy: %v", retention.Until)
	}

	// A feature that keeps nothing has no schedule to state.
	none := NewPolicy([]Feature{{
		Name: FeatureCopilot, Enabled: true, Provider: "a", Model: "b", RetentionDays: 30,
	}}, nil, spend{})
	if kept := none.RetentionFor(FeatureCopilot, now, false); kept.KeepOutput ||
		!kept.Until.IsZero() {
		t.Fatalf("a feature that keeps nothing produced a schedule: %+v", kept)
	}
}

// A hold is an instruction not to delete, not an instruction to collect.
func TestALegalHoldNeverCausesCollection(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	policy := NewPolicy([]Feature{enabled()}, nil, spend{})

	held := policy.RetentionFor(FeatureSupportReply, now, true)
	if held.KeepPrompt {
		t.Fatal("a hold caused a prompt to be kept that the policy said not to store")
	}
	if !held.KeepOutput || !held.Until.IsZero() {
		t.Fatalf("a hold did not suspend deletion: %+v", held)
	}
}
