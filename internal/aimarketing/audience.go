package aimarketing

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/omniflow/omniflow/internal/aigateway"
)

// Audience-aware drafting.
//
// Copy written for "customers whose trial lapsed last week" is better than copy
// written for nobody, and getting that improvement without handing a model a
// list of customers is the whole problem.
//
// The answer is that an audience reaches this package as a description, never
// as members. There is no field for a recipient, an identifier, or a query —
// only the segment's name, the criteria that define it in the words the segment
// editor already shows an operator, a size band, and rounded trait shares.
//
// The size band matters more than it looks. An exact count is an identifier
// when it is small: "the 1 customer on the Pro plan in Portugal" describes a
// person as precisely as their name does. Banding removes that, and a floor
// below which audience-aware drafting simply refuses removes the rest — with
// four people, the criteria alone are a description of those four.

// ErrAudienceTooSmall reports a segment too small to describe in aggregate
// without describing its members.
var ErrAudienceTooSmall = errors.New("this segment is too small for audience-aware drafting")

// MinAudienceForDrafting is the floor. Twenty-five is not a magic number; it is
// small enough that ordinary segments qualify and large enough that a band and
// a set of rounded shares cannot be resolved back to individuals.
const MinAudienceForDrafting = 25

// Audience is an aggregate description of who a message is for.
//
// Every field is a statement about the group. There is deliberately no field
// that could hold a customer, so "the audience leaked" is not a mistake a
// caller can make here — it would require adding a field.
type Audience struct {
	// SegmentName is the operator's own label for the segment.
	SegmentName string
	// Criteria are the segment's filters in the words the segment editor shows.
	// They are the operator's own definition, which is why they are safe: an
	// operator who wrote "trial ended in the last 7 days" already knows it.
	Criteria []string
	// SizeBand is a bucket, never a count.
	SizeBand string
	// Traits are rounded shares — "72% Russian", "40% on the annual plan".
	Traits []Trait
}

// Trait is one aggregate characteristic.
type Trait struct {
	Name string
	// Share is a percentage rounded to the nearest ten. Finer resolution buys a
	// model nothing and starts to distinguish small groups.
	Share int
}

// SizeBandFor buckets a count.
//
// The bands widen as they go up because the difference between 30 and 40
// customers changes how you write; the difference between 30,000 and 40,000
// does not.
func SizeBandFor(count int) string {
	switch {
	case count < MinAudienceForDrafting:
		return "under 25"
	case count < 100:
		return "25–100"
	case count < 500:
		return "100–500"
	case count < 2000:
		return "500–2,000"
	case count < 10000:
		return "2,000–10,000"
	default:
		return "over 10,000"
	}
}

// RoundShare rounds a percentage to the nearest ten, clamped to the range.
func RoundShare(percent int) int {
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return ((percent + 5) / 10) * 10
}

// DescribeAudience builds the aggregate description from a segment's own
// definition and counts.
//
// It refuses a segment below the floor rather than describing it, because with
// a handful of people the criteria are the members.
func DescribeAudience(
	name string, criteria []string, count int, traits map[string]int,
) (Audience, error) {
	if count < MinAudienceForDrafting {
		return Audience{}, fmt.Errorf("%w: %d customers", ErrAudienceTooSmall, count)
	}

	rounded := make([]Trait, 0, len(traits))
	for trait, share := range traits {
		// A trait almost nobody has is noise to a copywriter and a discriminator
		// to anybody trying to identify the group.
		if RoundShare(share) < 10 {
			continue
		}
		rounded = append(rounded, Trait{Name: trait, Share: RoundShare(share)})
	}
	sort.SliceStable(rounded, func(left, right int) bool {
		if rounded[left].Share != rounded[right].Share {
			return rounded[left].Share > rounded[right].Share
		}
		return rounded[left].Name < rounded[right].Name
	})

	cleaned := make([]string, 0, len(criteria))
	for _, criterion := range criteria {
		if trimmed := strings.TrimSpace(criterion); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}

	return Audience{
		SegmentName: strings.TrimSpace(name), Criteria: cleaned,
		SizeBand: SizeBandFor(count), Traits: rounded,
	}, nil
}

// describe renders the audience as prompt instruction.
//
// It is instruction rather than content because it is what the operator is
// asking for — their own segment definition, in their own words — and it is
// appended to the brief's purpose rather than to the untrusted parts.
func (audience Audience) describe() string {
	if audience.SegmentName == "" && len(audience.Criteria) == 0 {
		return ""
	}
	sentences := make([]string, 0, 3)
	if audience.SegmentName != "" {
		sentences = append(sentences,
			fmt.Sprintf("The audience is the %q segment (%s customers).",
				truncate(audience.SegmentName, 120), audience.SizeBand))
	}
	if len(audience.Criteria) > 0 {
		sentences = append(sentences,
			"They are defined by: "+strings.Join(clipEach(audience.Criteria, 8, 120), "; ")+".")
	}
	if len(audience.Traits) > 0 {
		shares := make([]string, 0, len(audience.Traits))
		for _, trait := range audience.Traits {
			shares = append(shares, fmt.Sprintf("%d%% %s", trait.Share, trait.Name))
		}
		sentences = append(sentences, "Roughly "+strings.Join(shares, ", ")+".")
	}
	// The model is told what it has, and told what it does not have, because a
	// model that assumes it knows a recipient writes as though it does.
	sentences = append(sentences,
		"You have no information about any individual in this audience and must "+
			"not write as though you do.")
	return " " + strings.Join(sentences, " ")
}

func clipEach(values []string, maxItems, maxLength int) []string {
	if len(values) > maxItems {
		values = values[:maxItems]
	}
	clipped := make([]string, 0, len(values))
	for _, value := range values {
		clipped = append(clipped, truncate(value, maxLength))
	}
	return clipped
}

// DraftFor writes copy for an aggregate audience.
//
// It is a separate entry point from Draft rather than a field on Brief, so the
// audience-free path stays audience-free: a caller that does not pass one
// cannot accidentally acquire one, and this function is the only place where
// segment information enters a prompt.
func (service *Service) DraftFor(
	ctx context.Context, brief Brief, audience Audience,
) (Draft, error) {
	if service.gateway == nil {
		return Draft{}, aigateway.ErrDisabled
	}
	if audience.SizeBand == SizeBandFor(0) {
		// An audience built by hand rather than through DescribeAudience could
		// still be under the floor. Checking here means there is no path to a
		// prompt that describes a handful of people.
		return Draft{}, ErrAudienceTooSmall
	}

	instruction := fmt.Sprintf(
		"Write a marketing message in %s. Purpose: %s.",
		languageName(brief.Language), truncate(brief.Purpose, 500),
	)
	instruction += audience.describe()
	if len(brief.Variables) > 0 {
		instruction += " You may use these variables exactly as written: " +
			strings.Join(wrapVariables(brief.Variables), ", ") + "."
	}
	if service.brandVoice != "" {
		instruction += " House style: " + truncate(service.brandVoice, 400)
	}
	instruction += fmt.Sprintf(" Keep it under %d characters.", limitOf(brief))

	return service.complete(ctx, instruction, nil, brief)
}
