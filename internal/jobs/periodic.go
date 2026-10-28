package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// Task is one whole-table pass that must run once per interval across the
// installation, not once per process: expiring orders, sweeping saved carts,
// charging renewals, re-polling payment intents.
//
// These used to be ticker goroutines, which ran once per replica. Each pass is
// written to be safe when repeated, but a second copy still doubled the
// provider calls and raced the first over the same rows. As River periodic
// jobs they are enqueued by River's elected leader only, and the uniqueness
// below keeps a pass that outlasts its interval from being queued again
// beside itself.
type Task struct {
	// Name identifies the pass in job arguments, logs, and the dead-letter view.
	Name string
	// Interval is how often the leader enqueues the pass.
	Interval time.Duration
	// Timeout bounds one run. Zero uses the interval, so a stuck pass is
	// abandoned before the next one would have been due; a negative value
	// disables the timeout for a pass, such as a backup, whose duration is the
	// database's size rather than a property of the code.
	Timeout time.Duration
	// Run performs the pass. An error is recorded on the job and the next
	// interval tries again; nothing is retried in between, because the next
	// pass covers the same rows.
	Run func(context.Context) error
}

// PeriodicArgs names the task a periodic job runs.
type PeriodicArgs struct {
	Task string `json:"task"`
}

// Kind is the River job kind every periodic pass shares.
func (PeriodicArgs) Kind() string { return "periodic_task" }

// periodicUniqueStates holds a pass unique while it is waiting or running,
// and releases it once it finishes either way. Leaving completed out is what
// lets the next interval insert it again.
var periodicUniqueStates = []rivertype.JobState{
	rivertype.JobStateAvailable,
	rivertype.JobStatePending,
	rivertype.JobStateRetryable,
	rivertype.JobStateRunning,
	rivertype.JobStateScheduled,
}

// PeriodicWorker runs registered tasks by name.
type PeriodicWorker struct {
	river.WorkerDefaults[PeriodicArgs]
	tasks  map[string]Task
	logger *slog.Logger
}

// NewPeriodic validates the tasks and returns the worker that runs them and the
// schedule that enqueues them. Both must be given to the same worker client.
func NewPeriodic(logger *slog.Logger, tasks ...Task) (*PeriodicWorker, []*river.PeriodicJob, error) {
	if logger == nil {
		logger = slog.Default()
	}
	registry := make(map[string]Task, len(tasks))
	schedule := make([]*river.PeriodicJob, 0, len(tasks))
	for _, task := range tasks {
		switch {
		case task.Name == "":
			return nil, nil, errors.New("jobs: a periodic task needs a name")
		case task.Interval <= 0:
			return nil, nil, fmt.Errorf("jobs: periodic task %s needs a positive interval", task.Name)
		case task.Run == nil:
			return nil, nil, fmt.Errorf("jobs: periodic task %s has nothing to run", task.Name)
		}
		if _, duplicate := registry[task.Name]; duplicate {
			return nil, nil, fmt.Errorf("jobs: periodic task %s is registered twice", task.Name)
		}
		registry[task.Name] = task
		name := task.Name
		schedule = append(schedule, river.NewPeriodicJob(
			river.PeriodicInterval(task.Interval),
			func() (river.JobArgs, *river.InsertOpts) {
				return PeriodicArgs{Task: name}, PeriodicInsertOpts()
			},
			&river.PeriodicJobOpts{ID: name, RunOnStart: true},
		))
	}
	return &PeriodicWorker{tasks: registry, logger: logger}, schedule, nil
}

// PeriodicInsertOpts are the options every periodic pass is inserted with.
//
// One attempt: a failed pass is recorded, and the next interval runs a fresh
// one over the same rows, which is a better retry than River's backoff.
func PeriodicInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{
		Queue:       QueueDefault,
		MaxAttempts: 1,
		UniqueOpts:  river.UniqueOpts{ByArgs: true, ByState: periodicUniqueStates},
	}
}

// Timeout applies the task's own bound.
func (worker *PeriodicWorker) Timeout(job *river.Job[PeriodicArgs]) time.Duration {
	task, ok := worker.tasks[job.Args.Task]
	if !ok {
		return 0
	}
	if task.Timeout != 0 {
		return task.Timeout
	}
	return task.Interval
}

// Work runs one pass.
func (worker *PeriodicWorker) Work(ctx context.Context, job *river.Job[PeriodicArgs]) error {
	task, ok := worker.tasks[job.Args.Task]
	if !ok {
		// A job left behind by a release that had a task this one does not.
		// Retrying cannot make the task exist.
		return river.JobCancel(fmt.Errorf("unknown periodic task %q", job.Args.Task))
	}
	started := time.Now()
	if err := task.Run(ctx); err != nil {
		worker.logger.Error("periodic task failed", "task", task.Name, "error", err)
		return err
	}
	worker.logger.Debug("periodic task finished", "task", task.Name, "duration", time.Since(started))
	return nil
}

// RunOnce runs a registered task immediately in the calling goroutine. It is
// what a test drives instead of waiting for River's leader to schedule it.
func (worker *PeriodicWorker) RunOnce(ctx context.Context, name string) error {
	task, ok := worker.tasks[name]
	if !ok {
		return fmt.Errorf("unknown periodic task %q", name)
	}
	return task.Run(ctx)
}

// Tasks lists the registered task names.
func (worker *PeriodicWorker) Tasks() []string {
	names := make([]string, 0, len(worker.tasks))
	for name := range worker.tasks {
		names = append(names, name)
	}
	return names
}
