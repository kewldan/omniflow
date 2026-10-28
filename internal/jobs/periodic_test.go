package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/config"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func noop(context.Context) error { return nil }

func TestPeriodicTasksAreValidated(t *testing.T) {
	cases := map[string][]Task{
		"no name":     {{Interval: time.Minute, Run: noop}},
		"no interval": {{Name: "a", Run: noop}},
		"no run":      {{Name: "a", Interval: time.Minute}},
		"duplicate":   {{Name: "a", Interval: time.Minute, Run: noop}, {Name: "a", Interval: time.Hour, Run: noop}},
	}
	for name, tasks := range cases {
		if _, _, err := NewPeriodic(nil, tasks...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPeriodicWorkerRunsTasksByName(t *testing.T) {
	ran := 0
	failure := errors.New("pass failed")
	worker, schedule, err := NewPeriodic(nil,
		Task{Name: "counted", Interval: time.Minute, Run: func(context.Context) error { ran++; return nil }},
		Task{Name: "failing", Interval: time.Hour, Timeout: -1, Run: func(context.Context) error { return failure }},
	)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(schedule) != 2 {
		t.Fatalf("schedule has %d jobs, want 2", len(schedule))
	}
	if err = worker.Work(t.Context(), &river.Job[PeriodicArgs]{Args: PeriodicArgs{Task: "counted"}}); err != nil || ran != 1 {
		t.Fatalf("work: %v, ran %d", err, ran)
	}
	if err = worker.Work(t.Context(), &river.Job[PeriodicArgs]{Args: PeriodicArgs{Task: "failing"}}); !errors.Is(err, failure) {
		t.Fatalf("a failing pass reported %v", err)
	}
	// A job left behind by an older release is cancelled, not retried.
	var cancel *river.JobCancelError
	if err = worker.Work(t.Context(), &river.Job[PeriodicArgs]{Args: PeriodicArgs{Task: "gone"}}); !errors.As(err, &cancel) {
		t.Fatalf("an unknown task reported %v", err)
	}
	if got := worker.Timeout(&river.Job[PeriodicArgs]{Args: PeriodicArgs{Task: "counted"}}); got != time.Minute {
		t.Fatalf("default timeout = %v, want the interval", got)
	}
	if got := worker.Timeout(&river.Job[PeriodicArgs]{Args: PeriodicArgs{Task: "failing"}}); got != -1 {
		t.Fatalf("explicit timeout = %v, want -1", got)
	}
}

// The uniqueness states must satisfy River's own rule, and must leave a
// completed pass out so the next interval can insert it again.
func TestPeriodicUniquenessReleasesAFinishedPass(t *testing.T) {
	options := PeriodicInsertOpts()
	for _, required := range []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending,
		rivertype.JobStateRunning, rivertype.JobStateScheduled,
	} {
		if !contains(options.UniqueOpts.ByState, required) {
			t.Fatalf("unique states miss %s, which River requires", required)
		}
	}
	if contains(options.UniqueOpts.ByState, rivertype.JobStateCompleted) {
		t.Fatal("a completed pass would block every later one")
	}
	if options.MaxAttempts != 1 || options.Queue != QueueDefault {
		t.Fatalf("insert options = %+v", options)
	}
}

// The worker refuses a pool that cannot carry its own job concurrency plus
// River's bookkeeping, so the default must clear that bar.
func TestTheDefaultWorkerPoolCarriesTheJobConcurrency(t *testing.T) {
	if int(config.DefaultWorkerDatabaseMaxConns) < MaxWorkers()+4 {
		t.Fatalf("default worker pool %d cannot carry %d jobs", config.DefaultWorkerDatabaseMaxConns, MaxWorkers())
	}
}

func contains(states []rivertype.JobState, want rivertype.JobState) bool {
	for _, state := range states {
		if state == want {
			return true
		}
	}
	return false
}
