package aieval

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// The set that ships is the one an installation runs against its own provider,
// so it has to load cleanly here first.
func TestEveryShippedSuiteLoads(t *testing.T) {
	suites, err := Suites()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, expected := range []string{
		"support_reply", "marketing", "risk_analysis", "translation",
	} {
		suite, present := suites[expected]
		if !present {
			t.Fatalf("the %s suite is missing: %v", expected, SuiteNames())
		}
		if len(suite.Cases) < 3 {
			t.Fatalf("%s has only %d cases, which measures very little",
				expected, len(suite.Cases))
		}
		for _, candidate := range suite.Cases {
			if strings.TrimSpace(candidate.Note) == "" {
				t.Fatalf("%s/%s has no note, so a failure explains nothing",
					expected, candidate.ID)
			}
		}
	}
}

// An evaluation set is exactly the kind of file that accumulates a real ticket
// somebody pasted in while debugging.
func TestACaseCarryingRealMaterialIsRefused(t *testing.T) {
	// Assembled at runtime rather than written as a literal, so the compiled
	// test binary carries no credential-shaped string.
	card := strings.Join([]string{"4111", "1111", "1111", "1111"}, " ")
	document := `[{"id":"leaky","feature":"support_reply","input":"customer card ` +
		card + ` was charged","checks":[{"dimension":"correctness"}]}]`

	if _, err := Load("leaky", []byte(document)); !errors.Is(err, ErrUnsanitized) {
		t.Fatalf("expected ErrUnsanitized, got %v", err)
	}
}

// A partially loaded evaluation reports a pass rate over the cases that
// happened to parse, which is a number that looks like a result and is not.
func TestAnUnusableCaseRefusesTheWholeSuite(t *testing.T) {
	for _, document := range []string{
		`[{"id":"","feature":"support_reply","checks":[{"dimension":"correctness"}]}]`,
		`[{"id":"a","feature":"","checks":[{"dimension":"correctness"}]}]`,
		`[{"id":"a","feature":"support_reply","checks":[]}]`,
		`[{"id":"a","feature":"support_reply","checks":[{"dimension":"vibes"}]}]`,
		`[{"id":"a","feature":"support_reply","checks":[{"dimension":"tone","mustMatch":"([a"}]}]`,
		`[]`,
	} {
		if _, err := Load("broken", []byte(document)); !errors.Is(err, ErrUnusableCase) {
			t.Fatalf("%s was accepted: %v", document, err)
		}
	}
}

func candidate(checks ...Check) Case {
	return Case{ID: "c-1", Feature: "support_reply", Checks: checks}
}

// A failure names the specific expectation, so it is actionable rather than a
// red mark.
func TestGradingNamesWhatWentWrong(t *testing.T) {
	result := Grade(
		candidate(Check{
			Dimension: DimensionSafety, MustNotContain: []string{"guaranteed"},
		}),
		Answer{Text: "This is guaranteed to work."},
	)
	if result.Passed() {
		t.Fatal("a forbidden phrase passed")
	}
	if !strings.Contains(result.Failures[0].Reason, "guaranteed") {
		t.Fatalf("the failure does not name the phrase: %+v", result.Failures[0])
	}
	if result.Failures[0].Dimension != DimensionSafety {
		t.Fatalf("the failure is on the wrong dimension: %+v", result.Failures[0])
	}
}

// "Cites at least one source" survives a model that words things differently;
// a golden string does not.
func TestChecksArePropertiesRatherThanExactText(t *testing.T) {
	check := Check{Dimension: DimensionCitations, MustCite: 1, MustMatch: `\[S\d\]`}
	compiled, err := Load("t", []byte(
		`[{"id":"c","feature":"f","checks":[{"dimension":"citation_validity",`+
			`"mustCite":1,"mustMatch":"\\[S\\d\\]"}]}]`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	_ = check

	for _, phrasing := range []string{
		"We refunded the duplicate charge [S1].",
		"The duplicate charge was reversed, see [S2].",
	} {
		if result := Grade(compiled.Cases[0], Answer{Text: phrasing, Citations: 1}); !result.Passed() {
			t.Fatalf("%q failed: %+v", phrasing, result.Failures)
		}
	}
	if result := Grade(
		compiled.Cases[0], Answer{Text: "We refunded it.", Citations: 0},
	); result.Passed() {
		t.Fatal("an uncited answer passed a citation check")
	}
}

// A refusal is a valid answer for a case that expects one and a failure for a
// case that does not.
func TestRefusalIsGradedBothWays(t *testing.T) {
	expected := candidate(Check{Dimension: DimensionRefusal, MustRefuse: true})
	if result := Grade(expected, Answer{Refused: true}); !result.Passed() {
		t.Fatalf("a correct refusal failed: %+v", result.Failures)
	}
	if result := Grade(expected, Answer{Text: "Sure, here you go."}); result.Passed() {
		t.Fatal("answering a case that should be refused passed")
	}

	ordinary := candidate(Check{Dimension: DimensionCorrectness, MustContain: []string{"renew"}})
	result := Grade(ordinary, Answer{Refused: true})
	if result.Passed() {
		t.Fatal("refusing an ordinary case passed")
	}
	if !strings.Contains(result.Failures[0].Reason, "should have answered") {
		t.Fatalf("the failure does not explain the refusal: %+v", result.Failures[0])
	}
}

// A case that fails one check is a case an operator would have had to fix, so
// the overall rate is stricter than any per-dimension rate.
func TestTheOverallRateIsStricterThanTheDimensions(t *testing.T) {
	suite := &Suite{Name: "s", Cases: []Case{
		{ID: "a", Feature: "f", Checks: []Check{
			{Dimension: DimensionTone}, {Dimension: DimensionSafety},
		}},
		{ID: "b", Feature: "f", Checks: []Check{
			{Dimension: DimensionTone}, {Dimension: DimensionSafety},
		}},
	}}
	report := Score(suite, []Result{
		{CaseID: "a", Feature: "f"},
		{CaseID: "b", Feature: "f", Failures: []Failure{
			{CaseID: "b", Dimension: DimensionTone, Reason: "too formal"},
		}},
	})

	if report.Overall != 0.5 {
		t.Fatalf("overall was %v, expected 0.5", report.Overall)
	}
	if report.Rates[DimensionTone] != 0.5 {
		t.Fatalf("tone was %v, expected 0.5", report.Rates[DimensionTone])
	}
	if report.Rates[DimensionSafety] != 1 {
		t.Fatalf("safety was %v, expected 1", report.Rates[DimensionSafety])
	}
}

// "We did not test tone" and "tone was perfect" must not look the same on a
// dashboard.
func TestAnUntestedDimensionIsNotAPass(t *testing.T) {
	suite := &Suite{Name: "s", Cases: []Case{
		{ID: "a", Feature: "f", Checks: []Check{{Dimension: DimensionSafety}}},
	}}
	report := Score(suite, []Result{{CaseID: "a", Feature: "f"}})

	if _, present := report.Rates[DimensionTone]; present {
		t.Fatal("an unexercised dimension reported a rate")
	}
	untested := report.Untested(DefaultThresholds())
	if !slices.Contains(untested, DimensionTone) {
		t.Fatalf("the untested dimension was not surfaced: %v", untested)
	}
	// And it produces no regression, so a caller can tell the two apart.
	for _, regression := range report.Regressions(DefaultThresholds()) {
		if regression.Dimension == DimensionTone {
			t.Fatal("an untested dimension was reported as a regression")
		}
	}
}

// A slightly off tone is a nuisance; unsafe advice reaching a customer is not.
func TestSafetyAndGroundednessHaveNoMargin(t *testing.T) {
	thresholds := DefaultThresholds()
	for _, absolute := range []string{
		DimensionSafety, DimensionGroundedness, DimensionCitations,
		DimensionConstraints, DimensionRefusal,
	} {
		if thresholds.Dimensions[absolute] != 1 {
			t.Fatalf("%s has a margin: %v", absolute, thresholds.Dimensions[absolute])
		}
	}
	if thresholds.Dimensions[DimensionTone] >= 1 {
		t.Fatalf("tone has no margin, which is a threshold nobody can hold: %v",
			thresholds.Dimensions[DimensionTone])
	}
}

// A regression names the dimension, what it got, and what it needed.
func TestRegressionsAreReportedWithBothNumbers(t *testing.T) {
	suite := &Suite{Name: "s", Cases: []Case{
		{ID: "a", Feature: "f", Checks: []Check{{Dimension: DimensionSafety}}},
	}}
	report := Score(suite, []Result{{CaseID: "a", Feature: "f", Failures: []Failure{
		{CaseID: "a", Dimension: DimensionSafety, Reason: "told a customer to share a code"},
	}}})

	regressions := report.Regressions(DefaultThresholds())
	if len(regressions) != 2 {
		t.Fatalf("expected the overall and the safety regression, got %+v", regressions)
	}
	found := false
	for _, regression := range regressions {
		if regression.Dimension == DimensionSafety {
			found = true
			if regression.Got != 0 || regression.Want != 1 {
				t.Fatalf("the numbers are wrong: %+v", regression)
			}
		}
	}
	if !found {
		t.Fatalf("safety was not reported: %+v", regressions)
	}
}

// A clean run produces nothing to act on.
func TestACleanRunHasNoRegressions(t *testing.T) {
	suite := &Suite{Name: "s", Cases: []Case{
		{ID: "a", Feature: "f", Checks: []Check{
			{Dimension: DimensionSafety}, {Dimension: DimensionTone},
		}},
	}}
	report := Score(suite, []Result{{CaseID: "a", Feature: "f"}})
	if regressions := report.Regressions(DefaultThresholds()); len(regressions) != 0 {
		t.Fatalf("a clean run reported regressions: %+v", regressions)
	}
}

// Every shipped suite exercises at least one absolute dimension, so a run
// cannot come back green having measured only taste.
func TestTheShippedSuitesExerciseTheAbsoluteDimensions(t *testing.T) {
	suites, err := Suites()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	exercised := map[string]bool{}
	for _, suite := range suites {
		for _, candidate := range suite.Cases {
			for _, check := range candidate.Checks {
				exercised[check.Dimension] = true
			}
		}
	}
	for _, required := range []string{
		DimensionGroundedness, DimensionSafety, DimensionConstraints,
		DimensionCorrectness, DimensionCitations,
	} {
		if !exercised[required] {
			t.Fatalf("no shipped case exercises %s", required)
		}
	}
}
