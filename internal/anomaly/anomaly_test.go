package anomaly

import (
	"testing"
	"time"
)

func hourRule() Rule {
	return Rule{
		Metric: MetricPurchase, Enabled: true, Window: time.Hour,
		WarnThreshold: 100, AlertThreshold: 500, MinimumSample: 2,
	}
}

func TestEvaluateClassifiesAgainstBothThresholds(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 30, 0, 0, time.UTC)
	signals := Evaluate(hourRule(), now, []Observation{
		{SubjectType: SubjectCustomer, SubjectID: "quiet", Value: 99, Sample: 5},
		{SubjectType: SubjectCustomer, SubjectID: "warm", Value: 100, Sample: 5},
		{SubjectType: SubjectCustomer, SubjectID: "hot", Value: 900, Sample: 5},
	})

	if len(signals) != 2 {
		t.Fatalf("expected the below-threshold observation to be dropped, got %d signals", len(signals))
	}
	// Ordered by observation, so the worst offender is first.
	if signals[0].SubjectID != "hot" || signals[0].Severity != SeverityAlert {
		t.Fatalf("expected the highest observation to lead as an alert, got %+v", signals[0])
	}
	if signals[0].Threshold != 500 {
		t.Fatalf("an alert must report the alert threshold it crossed, got %d", signals[0].Threshold)
	}
	if signals[1].Severity != SeverityWarning || signals[1].Threshold != 100 {
		t.Fatalf("expected a warning at the warn threshold, got %+v", signals[1])
	}
}

func TestEvaluateHonoursMinimumSample(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	// Far above the alert threshold, but on too little evidence to mean anything.
	signals := Evaluate(hourRule(), now, []Observation{
		{SubjectType: SubjectCustomer, SubjectID: "new", Value: 10_000, Sample: 1},
	})
	if len(signals) != 0 {
		t.Fatalf("a window below the minimum sample must raise nothing, got %+v", signals)
	}
}

func TestEvaluateSkipsDisabledAndIncoherentRules(t *testing.T) {
	now := time.Now().UTC()
	observations := []Observation{{SubjectID: "any", Value: 1_000_000, Sample: 100}}

	disabled := hourRule()
	disabled.Enabled = false
	if signals := Evaluate(disabled, now, observations); len(signals) != 0 {
		t.Fatalf("a disabled rule must raise nothing, got %d", len(signals))
	}

	inverted := hourRule()
	inverted.AlertThreshold = inverted.WarnThreshold - 1
	if signals := Evaluate(inverted, now, observations); len(signals) != 0 {
		t.Fatalf("a rule whose alert sits below its warning is incoherent and must be skipped")
	}

	unwindowed := hourRule()
	unwindowed.Window = 0
	if signals := Evaluate(unwindowed, now, observations); len(signals) != 0 {
		t.Fatalf("a rule with no window must be skipped rather than divide by zero")
	}
}

func TestDedupeKeyIsStableWithinAWindowAndChangesAcross(t *testing.T) {
	rule := hourRule()
	base := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	observation := Observation{SubjectType: SubjectCustomer, SubjectID: "same", Value: 900, Sample: 9}

	first := Evaluate(rule, base.Add(5*time.Minute), []Observation{observation})
	second := Evaluate(rule, base.Add(40*time.Minute), []Observation{observation})
	next := Evaluate(rule, base.Add(90*time.Minute), []Observation{observation})

	if first[0].DedupeKey != second[0].DedupeKey {
		t.Fatalf("two sweeps inside one window must share a dedupe key: %q vs %q",
			first[0].DedupeKey, second[0].DedupeKey)
	}
	if next[0].DedupeKey == first[0].DedupeKey {
		t.Fatalf("a later window must produce a new dedupe key, got %q twice", next[0].DedupeKey)
	}
}

func TestEvaluateDoesNotShareEvidenceMapsBetweenSignals(t *testing.T) {
	now := time.Now().UTC()
	shared := map[string]any{"orderCount": 4}
	signals := Evaluate(hourRule(), now, []Observation{
		{SubjectType: SubjectCustomer, SubjectID: "a", Value: 600, Sample: 4, Evidence: shared},
		{SubjectType: SubjectCustomer, SubjectID: "b", Value: 200, Sample: 4, Evidence: shared},
	})

	if len(signals) != 2 {
		t.Fatalf("expected both observations to raise, got %d", len(signals))
	}
	if signals[0].Threshold == signals[1].Threshold {
		t.Fatalf("the two signals crossed different thresholds and must report them separately")
	}
	if signals[0].Evidence["threshold"] == signals[1].Evidence["threshold"] {
		t.Fatalf("evidence maps were shared: both signals report threshold %v",
			signals[0].Evidence["threshold"])
	}
	if _, mutated := shared["threshold"]; mutated {
		t.Fatalf("the caller's evidence map must not be mutated")
	}
}

func TestEvaluateDefaultsSubjectTypeAndSkipsAnonymousSubjects(t *testing.T) {
	now := time.Now().UTC()
	signals := Evaluate(hourRule(), now, []Observation{
		{SubjectID: "", Value: 900, Sample: 9},
		{SubjectID: "installation-wide", Value: 900, Sample: 9},
	})
	if len(signals) != 1 {
		t.Fatalf("an observation with no subject cannot be reviewed and must be dropped")
	}
	if signals[0].SubjectType != SubjectInstallation {
		t.Fatalf("expected the installation default, got %q", signals[0].SubjectType)
	}
}
