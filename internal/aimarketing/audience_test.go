package aimarketing

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func segment() Audience {
	audience, err := DescribeAudience(
		"Lapsed trials",
		[]string{"trial ended in the last 7 days", "no active subscription"},
		420,
		map[string]int{"Russian speakers": 72, "was on the monthly plan": 38, "in Portugal": 3},
	)
	if err != nil {
		panic(err)
	}
	return audience
}

// "The 1 customer on the Pro plan in Portugal" describes a person as precisely
// as their name does.
func TestASegmentTooSmallToDescribeIsRefused(t *testing.T) {
	_, err := DescribeAudience("Tiny", []string{"in Portugal"}, 4, map[string]int{"Pro plan": 100})
	if !errors.Is(err, ErrAudienceTooSmall) {
		t.Fatalf("expected ErrAudienceTooSmall, got %v", err)
	}

	service, provider := newService(t, "copy")
	_, err = service.DraftFor(context.Background(), Brief{Purpose: "win them back"}, Audience{
		SegmentName: "Tiny", SizeBand: SizeBandFor(4),
	})
	if !errors.Is(err, ErrAudienceTooSmall) {
		t.Fatalf("a hand-built undersized audience reached drafting: %v", err)
	}
	if provider.seen.Prompt != "" {
		t.Fatal("an undersized audience reached the provider")
	}
}

// An exact count is an identifier when it is small, so the band is what travels.
func TestTheCountIsBandedRatherThanStated(t *testing.T) {
	audience := segment()
	if audience.SizeBand != "100–500" {
		t.Fatalf("the band is wrong: %q", audience.SizeBand)
	}

	service, provider := newService(t, "copy")
	if _, err := service.DraftFor(
		context.Background(), Brief{Purpose: "win them back"}, audience,
	); err != nil {
		t.Fatalf("draft: %v", err)
	}
	if strings.Contains(provider.seen.Prompt, "420") {
		t.Fatalf("the exact count reached the prompt: %q", provider.seen.Prompt)
	}
	if !strings.Contains(provider.seen.Prompt, "100–500") {
		t.Fatalf("the band did not reach the prompt: %q", provider.seen.Prompt)
	}
}

// Finer resolution buys a model nothing and starts to distinguish small groups.
func TestTraitSharesAreRoundedAndTinyOnesDropped(t *testing.T) {
	audience := segment()
	for _, trait := range audience.Traits {
		if trait.Share%10 != 0 {
			t.Fatalf("a share was not rounded: %+v", trait)
		}
		if trait.Name == "in Portugal" {
			t.Fatalf("a 3%% trait survived rounding: %+v", trait)
		}
	}
	if len(audience.Traits) != 2 {
		t.Fatalf("expected two traits, got %+v", audience.Traits)
	}
	// Biggest share first, so a copywriter reads what matters.
	if audience.Traits[0].Share < audience.Traits[1].Share {
		t.Fatalf("traits are not ordered by share: %+v", audience.Traits)
	}
}

// The segment's own criteria are safe because the operator wrote them; the
// members are not, and there is no field that could carry one.
func TestTheCriteriaTravelAndTheMembersCannot(t *testing.T) {
	service, provider := newService(t, "copy")
	if _, err := service.DraftFor(
		context.Background(), Brief{Purpose: "win them back", Language: "ru"}, segment(),
	); err != nil {
		t.Fatalf("draft: %v", err)
	}
	prompt := provider.seen.Prompt
	for _, must := range []string{
		"Lapsed trials", "trial ended in the last 7 days", "Russian",
	} {
		if !strings.Contains(prompt, must) {
			t.Fatalf("the audience definition did not reach the prompt: %q", must)
		}
	}
	// A model that assumes it knows a recipient writes as though it does.
	if !strings.Contains(prompt, "no information about any individual") {
		t.Fatalf("the prompt does not state what the model does not have: %q", prompt)
	}
}

// A caller that does not pass an audience cannot accidentally acquire one.
func TestTheAudienceFreePathStaysAudienceFree(t *testing.T) {
	service, provider := newService(t, "copy")
	if _, err := service.Draft(
		context.Background(), Brief{Purpose: "announce a plan"},
	); err != nil {
		t.Fatalf("draft: %v", err)
	}
	if strings.Contains(provider.seen.Prompt, "audience") {
		t.Fatalf("an ordinary draft mentioned an audience: %q", provider.seen.Prompt)
	}
}

// The bands widen as they go up because the difference between 30 and 40
// customers changes how you write and 30,000 versus 40,000 does not.
func TestBandsCoverTheRange(t *testing.T) {
	for count, band := range map[int]string{
		0: "under 25", 24: "under 25", 25: "25–100", 99: "25–100",
		100: "100–500", 1999: "500–2,000", 9999: "2,000–10,000", 50000: "over 10,000",
	} {
		if actual := SizeBandFor(count); actual != band {
			t.Fatalf("%d banded as %q, expected %q", count, actual, band)
		}
	}
}

// The checks that apply to any other copy apply here too.
func TestAudienceAwareCopyIsStillChecked(t *testing.T) {
	service, _ := newService(t, "Guaranteed to win you back, {{unknown}}!")
	draft, err := service.DraftFor(context.Background(), Brief{
		Purpose: "win back lapsed trials", Variables: []string{"first_name"},
	}, segment())
	if err != nil {
		t.Fatalf("draft: %v", err)
	}
	if draft.Acceptable() {
		t.Fatalf("audience-aware copy skipped the checks: %+v", draft.Findings)
	}
}
