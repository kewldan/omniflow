package jobs

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/riverqueue/river"
)

func TestJobOutcome(t *testing.T) {
	cases := map[string]error{
		"succeeded": nil,
		"cancelled": river.JobCancel(errors.New("gone")),
		"snoozed":   river.JobSnooze(time.Minute),
		"timeout":   fmt.Errorf("wrapped: %w", context.DeadlineExceeded),
		"failed":    errors.New("boom"),
	}
	for want, err := range cases {
		if got := JobOutcome(err); got != want {
			t.Errorf("JobOutcome(%v) = %q, want %q", err, got, want)
		}
	}
}

func TestPeriodicWorkerReportsEveryRun(t *testing.T) {
	failure := errors.New("boom")
	worker, _, err := NewPeriodic(nil,
		Task{Name: "ok", Interval: time.Minute, Run: func(context.Context) error { return nil }},
		Task{Name: "bad", Interval: time.Minute, Run: func(context.Context) error { return failure }},
	)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]error{}
	worker.WithObserver(func(name string, err error, _ time.Duration) { seen[name] = err })
	_ = worker.Work(context.Background(), &river.Job[PeriodicArgs]{Args: PeriodicArgs{Task: "ok"}})
	_ = worker.Work(context.Background(), &river.Job[PeriodicArgs]{Args: PeriodicArgs{Task: "bad"}})
	if seen["ok"] != nil || !errors.Is(seen["bad"], failure) || len(seen) != 2 {
		t.Fatalf("observed %v", seen)
	}
}
