package airisk

import (
	"strings"
	"testing"
	"time"
)

func moment(minute int) time.Time {
	return time.Date(2026, 8, 11, 10, minute, 0, 0, time.UTC)
}

func observation(kind, fingerprint, ticket, customer string, minute int) Observation {
	return Observation{
		Kind: kind, Fingerprint: fingerprint, TicketID: ticket,
		CustomerRef: customer, At: moment(minute),
	}
}

// Nine tickets across six accounts within an hour is a campaign, and it is
// invisible from inside any single conversation.
func TestACrossTicketClusterIsFound(t *testing.T) {
	observations := []Observation{
		observation(SignalPaymentMethod, "fp-1", "t-1", "c-1", 0),
		observation(SignalPaymentMethod, "fp-1", "t-2", "c-2", 1),
		observation(SignalPaymentMethod, "fp-1", "t-3", "c-3", 2),
		observation(SignalPaymentMethod, "fp-1", "t-4", "c-4", 3),
	}
	patterns := DetectPatterns(observations, PatternOptions{Window: time.Hour})
	if len(patterns) != 1 {
		t.Fatalf("expected one pattern, got %+v", patterns)
	}
	pattern := patterns[0]
	if pattern.Customers != 4 || len(pattern.TicketIDs) != 4 {
		t.Fatalf("the cluster was miscounted: %+v", pattern)
	}
	if !pattern.RecommendsReview() {
		t.Fatalf("a tight four-account cluster did not warrant a look: %+v", pattern)
	}
}

// People share households, offices, and payment cards, so two is noise.
func TestTwoAccountsAreNotAPattern(t *testing.T) {
	observations := []Observation{
		observation(SignalDeviceID, "fp-1", "t-1", "c-1", 0),
		observation(SignalDeviceID, "fp-1", "t-2", "c-2", 1),
		// The same customer appearing twice is one account, not two.
		observation(SignalDeviceID, "fp-1", "t-3", "c-1", 2),
	}
	if patterns := DetectPatterns(observations, PatternOptions{}); len(patterns) != 0 {
		t.Fatalf("two accounts produced a pattern: %+v", patterns)
	}
}

// Six accounts sharing a device over a year is a family; over four minutes it
// is not.
func TestTheWindowSeparatesAFamilyFromARing(t *testing.T) {
	spread := []Observation{
		observation(SignalDeviceID, "fp-1", "t-1", "c-1", 0),
		observation(SignalDeviceID, "fp-1", "t-2", "c-2", 600),
		observation(SignalDeviceID, "fp-1", "t-3", "c-3", 1200),
	}
	if patterns := DetectPatterns(spread, PatternOptions{
		Window: 5 * time.Minute,
	}); len(patterns) != 0 {
		t.Fatalf("observations outside the window correlated: %+v", patterns)
	}

	tight := []Observation{
		observation(SignalDeviceID, "fp-1", "t-1", "c-1", 0),
		observation(SignalDeviceID, "fp-1", "t-2", "c-2", 1),
		observation(SignalDeviceID, "fp-1", "t-3", "c-3", 2),
	}
	if patterns := DetectPatterns(tight, PatternOptions{
		Window: 5 * time.Minute,
	}); len(patterns) != 1 {
		t.Fatalf("a tight cluster was missed: %+v", patterns)
	}
}

// Different fingerprints are different patterns, and neither reveals the value
// it came from.
func TestPatternsAreSeparatedByFingerprintAndRevealNothing(t *testing.T) {
	observations := []Observation{
		observation(SignalIPPrefix, "fp-a", "t-1", "c-1", 0),
		observation(SignalIPPrefix, "fp-a", "t-2", "c-2", 1),
		observation(SignalIPPrefix, "fp-a", "t-3", "c-3", 2),
		observation(SignalIPPrefix, "fp-b", "t-4", "c-4", 0),
		observation(SignalIPPrefix, "fp-b", "t-5", "c-5", 1),
		observation(SignalIPPrefix, "fp-b", "t-6", "c-6", 2),
	}
	patterns := DetectPatterns(observations, PatternOptions{Window: time.Hour})
	if len(patterns) != 2 {
		t.Fatalf("expected two patterns, got %+v", patterns)
	}
	// The result names how many accounts, never which ones.
	for _, pattern := range patterns {
		for _, customer := range []string{"c-1", "c-4"} {
			if strings.Contains(pattern.Explanation, customer) {
				t.Fatalf("a customer reference reached operator-facing text: %q",
					pattern.Explanation)
			}
		}
	}
}

// Silently accepting an unrecognised kind is how an installation starts
// fingerprinting a field nobody decided was proportionate.
func TestAnUnrecognisedSignalKindIsDropped(t *testing.T) {
	observations := []Observation{
		observation("email_address", "fp-1", "t-1", "c-1", 0),
		observation("email_address", "fp-1", "t-2", "c-2", 1),
		observation("email_address", "fp-1", "t-3", "c-3", 2),
	}
	if patterns := DetectPatterns(observations, PatternOptions{}); len(patterns) != 0 {
		t.Fatalf("an unapproved signal kind was correlated: %+v", patterns)
	}
}

// Two observations of the same value produce the same fingerprint and nothing
// else about it.
func TestFingerprintsAreStableOpaqueAndSalted(t *testing.T) {
	first := Fingerprint("installation-salt", SignalPaymentMethod, "  Card-4242 ")
	second := Fingerprint("installation-salt", SignalPaymentMethod, "card-4242")
	if first != second || first == "" {
		t.Fatalf("the same value produced different fingerprints: %q %q", first, second)
	}
	if strings.Contains(first, "4242") {
		t.Fatalf("the value survived in the fingerprint: %q", first)
	}

	elsewhere := Fingerprint("another-salt", SignalPaymentMethod, "card-4242")
	if elsewhere == first {
		t.Fatal("fingerprints are comparable between installations")
	}
	otherKind := Fingerprint("installation-salt", SignalDeviceID, "card-4242")
	if otherKind == first {
		t.Fatal("the same value in two kinds produced one fingerprint")
	}
	if Fingerprint("salt", SignalDeviceID, "   ") != "" {
		t.Fatal("an empty value produced a fingerprint")
	}
}

// The explanation is arithmetic rather than inference, so an operator can
// disagree with it specifically.
func TestTheExplanationIsCheckable(t *testing.T) {
	observations := []Observation{
		observation(SignalReferralCode, "fp-1", "t-1", "c-1", 0),
		observation(SignalReferralCode, "fp-1", "t-2", "c-2", 1),
		observation(SignalReferralCode, "fp-1", "t-3", "c-3", 2),
	}
	patterns := DetectPatterns(observations, PatternOptions{Window: time.Hour})
	if len(patterns) != 1 {
		t.Fatalf("expected one pattern: %+v", patterns)
	}
	explanation := patterns[0].Explanation
	for _, must := range []string{"3 accounts", "3 tickets", "same referral code"} {
		if !strings.Contains(explanation, must) {
			t.Fatalf("the explanation is missing %q: %q", must, explanation)
		}
	}
	if strings.Contains(explanation, "m0s") {
		t.Fatalf("the span reads as a machine talking: %q", explanation)
	}
}

// An operator reading three findings reads the first one properly.
func TestTheTightestClustersComeFirst(t *testing.T) {
	observations := []Observation{
		// Three accounts, spread out.
		observation(SignalDeviceID, "loose", "t-1", "c-1", 0),
		observation(SignalDeviceID, "loose", "t-2", "c-2", 40),
		observation(SignalDeviceID, "loose", "t-3", "c-3", 50),
		// Five accounts, in a minute.
		observation(SignalDeviceID, "tight", "t-4", "c-4", 0),
		observation(SignalDeviceID, "tight", "t-5", "c-5", 0),
		observation(SignalDeviceID, "tight", "t-6", "c-6", 1),
		observation(SignalDeviceID, "tight", "t-7", "c-7", 1),
		observation(SignalDeviceID, "tight", "t-8", "c-8", 1),
	}
	patterns := DetectPatterns(observations, PatternOptions{Window: time.Hour})
	if len(patterns) != 2 {
		t.Fatalf("expected two patterns: %+v", patterns)
	}
	if patterns[0].Fingerprint != "tight" {
		t.Fatalf("the denser cluster is not first: %+v", patterns)
	}
	if patterns[0].Confidence != ConfidenceHigh {
		t.Fatalf("a five-account one-minute cluster was not high confidence: %+v", patterns[0])
	}
	if patterns[1].Confidence == ConfidenceHigh {
		t.Fatalf("a spread-out three-account cluster was high confidence: %+v", patterns[1])
	}
}

// A busy installation gets the tightest clusters rather than a page of
// coincidences.
func TestTheResultIsBounded(t *testing.T) {
	observations := make([]Observation, 0, 90)
	for group := range 30 {
		fingerprint := "fp-" + itoa(group)
		for account := range 3 {
			observations = append(observations, observation(
				SignalLinkHost, fingerprint,
				"t-"+itoa(group)+"-"+itoa(account),
				"c-"+itoa(group)+"-"+itoa(account), account,
			))
		}
	}
	patterns := DetectPatterns(observations, PatternOptions{Window: time.Hour, MaxPatterns: 5})
	if len(patterns) != 5 {
		t.Fatalf("the result was not bounded: %d", len(patterns))
	}
}
