// Package goodsdelivery is the durable worker that hands a paid digital-goods
// order to its provider and records what happened.
//
// It is separate from `internal/goods`, which holds the provider contract and
// the rules that have no database in them. This package owns the transaction
// boundaries, the retry schedule, and the one guarantee that matters: a paid
// order is submitted to the provider at most once.
package goodsdelivery

import (
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// JobArgs names the order to deliver.
//
// The order identifier is the unique key, so a duplicated enqueue — from a
// replayed webhook, a retried settlement, or an operator clicking twice —
// collapses into one job rather than two submissions.
type JobArgs struct {
	OrderID string `json:"orderId" river:"unique"`
}

// Kind identifies the job in River's queue.
func (JobArgs) Kind() string { return "goods_delivery" }

// InsertOpts places delivery on the critical queue.
//
// A customer has already paid by the time this runs, so it competes with
// subscription fulfillment rather than with background sweeps.
//
// MaxAttempts is deliberately higher than the delivery's own retry ceiling.
// River retries the *job* — including failures that never reached the provider,
// such as the database being briefly unavailable — while the attempt count on
// the delivery row governs how many times the provider itself is asked. The
// worker refuses to submit once that ceiling is reached, so the larger River
// budget cannot turn into extra purchases.
//
// Uniqueness covers every state in which a job for the order is still alive —
// waiting, scheduled, running, or retrying — and deliberately not a completed
// one. A completed job is how a retryable failure ends: the delivery row keeps
// the backoff, and the scheduler enqueues the next attempt when it is due. With
// River's default states, that completed job would have blocked the next
// attempt until it was pruned a day later. A second job can never run beside a
// live one, which is what keeps one order to one payment in flight.
func InsertOpts() *river.InsertOpts {
	return &river.InsertOpts{
		Queue:       "critical",
		MaxAttempts: 12,
		UniqueOpts:  river.UniqueOpts{ByArgs: true, ByState: liveStates},
	}
}

var liveStates = []rivertype.JobState{
	rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable,
	rivertype.JobStateRunning, rivertype.JobStateScheduled,
}
