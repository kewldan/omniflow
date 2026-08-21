package fulfillment

import (
	"testing"
	"time"
)

// The revival predicate decides which operations get a second job. It has to
// leave a healthy retry alone — River already holds a job for it — and catch
// the two cases where nothing does: a settlement that ran without a job
// inserter, and a job the queue discarded.
func TestStaleOperationSelectsOnlyAbandonedWork(t *testing.T) {
	before := time.Date(2026, time.August, 21, 12, 0, 0, 0, time.UTC)
	old := before.Add(-time.Minute)
	recent := before.Add(time.Minute)

	cases := []struct {
		name                     string
		status                   string
		createdAt, nextAttemptAt time.Time
		want                     bool
	}{
		{"pending with no job for longer than the window", "pending", old, old, true},
		{"retrying whose scheduled attempt passed long ago", "retrying", old, old, true},
		{"freshly created pending", "pending", recent, recent, false},
		{"retry scheduled for the future", "retrying", old, recent, false},
		{"already succeeded", "succeeded", old, old, false},
		{"cancelled", "cancelled", old, old, false},
		{"failed permanently", "failed", old, old, false},
		{"running right now", "running", old, old, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := StaleOperation(testCase.status, testCase.createdAt, testCase.nextAttemptAt, before); got != testCase.want {
				t.Fatalf("StaleOperation(%s) = %v, want %v", testCase.name, got, testCase.want)
			}
		})
	}
}

// A retry at the backoff ceiling is scheduled in the future, so it is never
// stale however long the ceiling is: staleness is measured from the attempt
// the worker itself scheduled, not from the operation's age.
func TestARetryAtTheBackoffCeilingIsNotStale(t *testing.T) {
	now := time.Date(2026, time.August, 21, 12, 0, 0, 0, time.UTC)
	createdAt := now.Add(-24 * time.Hour)
	nextAttemptAt := now.Add(backoff(50))
	if StaleOperation("retrying", createdAt, nextAttemptAt, now.Add(-staleOperationAge)) {
		t.Fatal("a retry the worker scheduled for later must not be re-queued")
	}
}
