package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"go.opentelemetry.io/otel/codes"

	"github.com/omniflow/omniflow/internal/platform"
)

// JobObserver is told how one worked job ended: its kind, a bounded outcome,
// and how long it ran.
type JobObserver func(kind, outcome string, elapsed time.Duration)

// ObserveMiddleware opens a span around every worked job and reports it to
// observe, except the kinds in skip — the periodic pass kind, which is
// reported per pass name instead, and any worker that already reports itself.
// Skipped kinds are still traced.
func ObserveMiddleware(observe JobObserver, skip ...string) rivertype.Middleware {
	skipped := make(map[string]bool, len(skip))
	for _, kind := range skip {
		skipped[kind] = true
	}
	return river.WorkerMiddlewareFunc(func(
		ctx context.Context, job *rivertype.JobRow, doInner func(context.Context) error,
	) error {
		ctx, span := platform.StartSpan(ctx, "river.job", platform.JobAttributes(job.Kind, job.Attempt)...)
		defer span.End()
		started := time.Now()
		err := doInner(ctx)
		outcome := JobOutcome(err)
		if outcome != "succeeded" && outcome != "snoozed" {
			span.SetStatus(codes.Error, outcome)
		}
		if observe != nil && !skipped[job.Kind] {
			observe(job.Kind, outcome, time.Since(started))
		}
		return err
	})
}

// JobOutcome classifies a worker's return value into a bounded label.
func JobOutcome(err error) string {
	var cancel *rivertype.JobCancelError
	var snooze *rivertype.JobSnoozeError
	switch {
	case err == nil:
		return "succeeded"
	case errors.As(err, &cancel):
		return "cancelled"
	case errors.As(err, &snooze):
		return "snoozed"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "failed"
	}
}
