// Package anomaly turns raw aggregates into reviewable signals.
//
// It exists as its own package, with no database, transport, or Telegram
// import, for the same reason `internal/rbac` does: the decision "is this
// observation worth an operator's attention" is a rule, it is easy to get
// subtly wrong, and it should be provable in a unit test rather than only
// observable in production.
//
// Two properties matter more than sensitivity here.
//
// A signal is evidence, never a sanction. Nothing in this package, and nothing
// that consumes it, suspends a customer, cancels an order, or moves money. It
// produces a row an operator reads, acknowledges, or dismisses.
//
// A condition that persists is one signal, not one per evaluation run. The
// dedupe key is derived from the metric, the subject, and the window bucket the
// observation falls in, so a customer who stays above a threshold for six hours
// produces one alert per window rather than one per sweep.
package anomaly

import (
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"
)

// Metric names the quantity a rule watches. The set is closed and matches the
// database check constraint on `anomaly_rules.metric`.
const (
	MetricTraffic  = "traffic"
	MetricPurchase = "purchase"
	MetricRefund   = "refund"
	MetricReferral = "referral"
)

// Severity distinguishes the two thresholds every rule carries.
const (
	SeverityWarning = "warning"
	SeverityAlert   = "alert"
)

// Subject types name what an observation is about.
const (
	SubjectInstallation = "installation"
	SubjectCustomer     = "customer"
	SubjectPlan         = "plan"
	SubjectProvider     = "provider"
)

// Rule is one operator-configured threshold pair.
//
// Both thresholds are expressed in the metric's own unit — bytes for traffic,
// minor units for purchase and refund, a plain count for referral — because
// converting them to a shared scale would mean the panel showing an operator a
// number they did not type.
type Rule struct {
	Metric         string
	Enabled        bool
	Window         time.Duration
	WarnThreshold  int64
	AlertThreshold int64
	// MinimumSample is the number of observations below which the window is too
	// small to say anything. It is what stops a new installation raising an
	// alert on its first three orders.
	MinimumSample int
}

// Valid reports whether a rule is coherent enough to evaluate. An incoherent
// rule is skipped rather than rejected: a single bad row must not stop the
// remaining metrics from being evaluated at all.
func (rule Rule) Valid() bool {
	return rule.Metric != "" &&
		rule.Window > 0 &&
		rule.WarnThreshold > 0 &&
		rule.AlertThreshold >= rule.WarnThreshold
}

// Observation is one aggregate the database produced for a subject.
//
// Evidence carries the numbers that explain the signal — counts, sums, the
// currency, the window — and never a message body, a link, a token, an address,
// or any other customer content. An operator reviewing a signal needs to see
// why it fired, which is arithmetic, not correspondence.
type Observation struct {
	SubjectType string
	SubjectID   string
	Value       int64
	Sample      int
	Evidence    map[string]any
}

// Signal is a raised anomaly, ready to be persisted and delivered.
type Signal struct {
	Metric      string
	Severity    string
	SubjectType string
	SubjectID   string
	Observed    int64
	Threshold   int64
	SampleSize  int
	WindowStart time.Time
	WindowEnd   time.Time
	Evidence    map[string]any
	DedupeKey   string
}

// Evaluate applies a rule to a window of observations.
//
// `now` is the end of the window and is passed in rather than read from the
// clock so a test, a replay, and a scheduled sweep all evaluate the same way.
// The result is ordered by descending observation so the most extreme signal in
// a batch is the first one an operator sees.
func Evaluate(rule Rule, now time.Time, observations []Observation) []Signal {
	if !rule.Enabled || !rule.Valid() {
		return nil
	}

	windowStart := now.Add(-rule.Window)
	bucket := bucketOf(now, rule.Window)

	signals := make([]Signal, 0, len(observations))
	for _, observation := range observations {
		if observation.SubjectID == "" {
			continue
		}
		if observation.Sample < rule.MinimumSample {
			continue
		}

		severity, threshold := classify(rule, observation.Value)
		if severity == "" {
			continue
		}

		signals = append(signals, Signal{
			Metric:      rule.Metric,
			Severity:    severity,
			SubjectType: defaultSubjectType(observation.SubjectType),
			SubjectID:   observation.SubjectID,
			Observed:    observation.Value,
			Threshold:   threshold,
			SampleSize:  observation.Sample,
			WindowStart: windowStart.UTC(),
			WindowEnd:   now.UTC(),
			Evidence:    withWindow(observation.Evidence, windowStart, now, threshold),
			DedupeKey:   DedupeKey(defaultSubjectType(observation.SubjectType), observation.SubjectID, bucket),
		})
	}

	sort.SliceStable(signals, func(left, right int) bool {
		return signals[left].Observed > signals[right].Observed
	})
	return signals
}

// classify returns the severity an observation earns, and the threshold it
// crossed to earn it. The alert band is checked first, so a value above both
// thresholds is an alert rather than a warning.
func classify(rule Rule, value int64) (string, int64) {
	switch {
	case value >= rule.AlertThreshold:
		return SeverityAlert, rule.AlertThreshold
	case value >= rule.WarnThreshold:
		return SeverityWarning, rule.WarnThreshold
	default:
		return "", 0
	}
}

// DedupeKey identifies "this subject, in this window" so a persisting condition
// updates one row instead of accumulating a new one per sweep.
//
// The bucket rather than the exact instant is what makes that work: two sweeps
// four minutes apart inside a one-hour window produce the same key, and the
// next hour produces a different one.
func DedupeKey(subjectType, subjectID string, bucket int64) string {
	return fmt.Sprintf("%s:%s:%d", subjectType, strings.TrimSpace(subjectID), bucket)
}

// bucketOf floors an instant onto a window-sized grid. A window that is not a
// whole number of seconds is rounded up to one, since a sub-second anomaly
// window is a misconfiguration rather than a use case.
func bucketOf(now time.Time, window time.Duration) int64 {
	seconds := int64(window / time.Second)
	if seconds <= 0 {
		seconds = 1
	}
	return now.UTC().Unix() / seconds
}

func defaultSubjectType(subjectType string) string {
	if subjectType == "" {
		return SubjectInstallation
	}
	return subjectType
}

// withWindow copies the caller's evidence and adds the window and the threshold
// that was crossed.
//
// It copies rather than mutates because the caller usually built the map from a
// database row it may still be using, and because an evidence map that grows
// during evaluation is exactly the kind of shared state that produces a signal
// carrying another subject's numbers.
func withWindow(evidence map[string]any, start, end time.Time, threshold int64) map[string]any {
	merged := make(map[string]any, len(evidence)+3)
	maps.Copy(merged, evidence)
	merged["windowStart"] = start.UTC().Format(time.RFC3339)
	merged["windowEnd"] = end.UTC().Format(time.RFC3339)
	merged["threshold"] = threshold
	return merged
}
