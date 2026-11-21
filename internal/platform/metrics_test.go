package platform

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestObservePassRecordsOutcomeAndLastSuccess(t *testing.T) {
	metrics := NewMetrics("worker")
	metrics.ObservePass("renewal", nil, time.Second)
	metrics.ObservePass("renewal", errors.New("boom"), time.Second)
	metrics.ObservePass("backup", context.DeadlineExceeded, time.Second)

	cases := map[[2]string]float64{
		{"renewal", "succeeded"}: 1,
		{"renewal", "failed"}:    1,
		{"backup", "timeout"}:    1,
		{"backup", "succeeded"}:  0,
	}
	for labels, want := range cases {
		if got := testutil.ToFloat64(metrics.PassRuns.WithLabelValues(labels[0], labels[1])); got != want {
			t.Errorf("runs %v = %v, want %v", labels, got, want)
		}
	}
	if got := testutil.ToFloat64(metrics.PassLastSuccess.WithLabelValues("renewal")); got < float64(time.Now().Add(-time.Minute).Unix()) {
		t.Errorf("renewal last success = %v, want about now", got)
	}
	if count := testutil.CollectAndCount(metrics.PassLastSuccess); count != 1 {
		t.Errorf("last-success series = %d, want only the pass that succeeded", count)
	}
}

func TestObserveRemnawaveCountsErrorsOnlyForFailures(t *testing.T) {
	metrics := NewMetrics("api")
	metrics.ObserveRemnawave("GET /api/users/:id", "not_found", "", time.Millisecond)
	metrics.ObserveRemnawave("GET /api/nodes", "failed", "remnawave_http_502", time.Millisecond)
	if got := testutil.ToFloat64(metrics.RemnawaveErrors.WithLabelValues("remnawave_http_502")); got != 1 {
		t.Errorf("errors = %v, want 1", got)
	}
	if count := testutil.CollectAndCount(metrics.RemnawaveErrors); count != 1 {
		t.Errorf("error series = %d, want 1 (a lookup miss is not an error)", count)
	}
}

func TestSetQueuesReplacesStaleStates(t *testing.T) {
	metrics := NewMetrics("worker")
	metrics.SetQueues([]QueueSample{{Queue: "critical", States: map[string]int64{"available": 3, "retryable": 1}, OldestAvailableS: 12}})
	metrics.SetQueues([]QueueSample{{Queue: "critical", States: map[string]int64{"available": 1}}})
	if count := testutil.CollectAndCount(metrics.QueueJobs); count != 1 {
		t.Errorf("queue series = %d, want 1 after the retryable state drained", count)
	}
	if got := testutil.ToFloat64(metrics.QueueJobs.WithLabelValues("critical", "available")); got != 1 {
		t.Errorf("available = %v, want 1", got)
	}
}

func TestNilMetricsHelpersAreSafe(t *testing.T) {
	var metrics *Metrics
	metrics.ObservePass("x", nil, 0)
	metrics.ObserveRemnawave("x", "failed", "x", 0)
	metrics.ObserveTelegramSend("sendMessage", "ok")
	metrics.ObserveTelegramUpdate("message", "handled")
	metrics.SetOutboxPending(1)
	metrics.SetQueues(nil)
	metrics.SetBuild("1.0.0")
}
