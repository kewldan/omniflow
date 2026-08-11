package commerce

import (
	"errors"
	"slices"
	"testing"
)

func TestSinglePolicyRefusesASecondSubscription(t *testing.T) {
	t.Parallel()
	policy := SubscriptionPolicy{}
	if err := policy.AllowAdditional(0, 0, nil); err != nil {
		t.Fatalf("the first subscription must always be allowed: %v", err)
	}
	err := policy.AllowAdditional(1, 0, nil)
	if !errors.Is(err, ErrSubscriptionRejected) {
		t.Fatalf("expected a rejection, got %v", err)
	}
	if reason := SubscriptionRejectionReason(err); reason != SubscriptionMultiDisabled {
		t.Fatalf("expected %q, got %q", SubscriptionMultiDisabled, reason)
	}
}

func TestMultiPolicyEnforcesItsOwnCeiling(t *testing.T) {
	t.Parallel()
	policy := SubscriptionPolicy{MultiEnabled: true, MaxPerCustomer: 3}
	if err := policy.AllowAdditional(2, 0, nil); err != nil {
		t.Fatalf("a third subscription must be allowed: %v", err)
	}
	err := policy.AllowAdditional(3, 0, nil)
	if reason := SubscriptionRejectionReason(err); reason != SubscriptionLimitReached {
		t.Fatalf("expected %q, got %q", SubscriptionLimitReached, reason)
	}
}

func TestPlanLimitAppliesIndependentlyOfTheGlobalLimit(t *testing.T) {
	t.Parallel()
	policy := SubscriptionPolicy{MultiEnabled: true, MaxPerCustomer: 5}
	planMax := 1
	err := policy.AllowAdditional(2, 1, &planMax)
	if reason := SubscriptionRejectionReason(err); reason != SubscriptionPlanLimit {
		t.Fatalf("expected %q, got %q", SubscriptionPlanLimit, reason)
	}
	if err := policy.AllowAdditional(2, 0, &planMax); err != nil {
		t.Fatalf("a different plan must still be allowed: %v", err)
	}
}

func TestEffectiveMaxIsOneWhenConcurrencyIsOff(t *testing.T) {
	t.Parallel()
	if got := (SubscriptionPolicy{MaxPerCustomer: 9}).EffectiveMax(); got != 1 {
		t.Fatalf("expected 1, got %d", got)
	}
	if got := (SubscriptionPolicy{MultiEnabled: true}).EffectiveMax(); got != 1 {
		t.Fatalf("an unset maximum must fall back to 1, got %d", got)
	}
}

func TestNormalizeSubscriptionLabelRejectsUnusableNames(t *testing.T) {
	t.Parallel()
	if _, err := NormalizeSubscriptionLabel("   "); err == nil {
		t.Fatal("a blank label must be refused")
	}
	if _, err := NormalizeSubscriptionLabel(string(make([]rune, 41))); err == nil {
		t.Fatal("an oversized label must be refused")
	}
	if _, err := NormalizeSubscriptionLabel("home\nwork"); err == nil {
		t.Fatal("a control character must be refused")
	}
	label, err := NormalizeSubscriptionLabel("  Домашняя  ")
	if err != nil || label != "Домашняя" {
		t.Fatalf("expected a trimmed label, got %q %v", label, err)
	}
}

func TestAutomaticSquadPolicyRefusesACustomerSelection(t *testing.T) {
	t.Parallel()
	policy := SquadPolicy{Selection: "automatic", Included: []string{"a", "b"}}
	resolved, err := policy.ResolveSquads(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !slices.Equal(resolved, []string{"a", "b"}) {
		t.Fatalf("expected the included set, got %v", resolved)
	}
	if _, err := policy.ResolveSquads([]string{"c"}); !errors.Is(err, ErrSquadSelection) {
		t.Fatal("an automatic plan must refuse a selection")
	}
}

func TestOptionalSquadPolicyValidatesTheSelection(t *testing.T) {
	t.Parallel()
	maximum := 2
	policy := SquadPolicy{Selection: "optional", Included: []string{"base"}, Offered: []string{"a", "b", "c"}, Maximum: &maximum}
	resolved, err := policy.ResolveSquads([]string{"b", "a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The result is deduplicated and ordered, so two equal selections produce
	// byte-identical entitlement state.
	if !slices.Equal(resolved, []string{"a", "b", "base"}) {
		t.Fatalf("expected a sorted union, got %v", resolved)
	}
	if _, err := policy.ResolveSquads([]string{"a", "b", "c"}); !errors.Is(err, ErrSquadSelection) {
		t.Fatal("selecting past the maximum must be refused")
	}
	if _, err := policy.ResolveSquads([]string{"unknown"}); !errors.Is(err, ErrSquadSelection) {
		t.Fatal("a squad the plan does not offer must be refused")
	}
}

func TestRequiredSquadPolicyDemandsAtLeastOne(t *testing.T) {
	t.Parallel()
	policy := SquadPolicy{Selection: "required", Offered: []string{"a", "b"}}
	_, err := policy.ResolveSquads(nil)
	if !errors.Is(err, ErrSquadSelection) {
		t.Fatal("a required selection must refuse an empty choice")
	}
	if _, err := policy.ResolveSquads([]string{"a"}); err != nil {
		t.Fatalf("one squad must satisfy a required selection: %v", err)
	}
}
