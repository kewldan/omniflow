package fulfillment

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// A claimed operation belongs to the run that claimed it until the lease ends,
// and to nobody after that. Only `running` carries a lease at all.
func TestLeaseHeldOnlyByARunningClaimInsideItsLease(t *testing.T) {
	now := time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC)
	if held, until := leaseHeld("running", now.Add(time.Minute), now); !held || !until.Equal(now.Add(time.Minute)) {
		t.Fatalf("a live claim must be held until its lease, got %v %s", held, until)
	}
	if held, _ := leaseHeld("running", now.Add(-time.Second), now); held {
		t.Fatal("a claim whose lease ended must be taken over")
	}
	for _, status := range []string{"pending", "retrying", "failed"} {
		if held, _ := leaseHeld(status, now.Add(time.Hour), now); held {
			t.Fatalf("a %s operation carries no lease", status)
		}
	}
}

// A create provisions what an order bought. A refunded order was given its
// money back and an unsettled one never paid; a partial refund still bought
// the product.
func TestOrderBlocksProvisioningOnlyWithoutAStandingPurchase(t *testing.T) {
	for state, want := range map[string]string{"refunded": "order_refunded", "cancelled": "order_not_paid", "expired": "order_not_paid"} {
		if reason, blocked := orderBlocksProvisioning(state); !blocked || reason != want {
			t.Fatalf("%s: (%q, %v), want (%q, true)", state, reason, blocked, want)
		}
	}
	for _, state := range []string{"paid", "fulfilled", "partially_refunded"} {
		if _, blocked := orderBlocksProvisioning(state); blocked {
			t.Fatalf("a %s order must be provisioned", state)
		}
	}
}

// Only a serialization failure is retried, and only a bounded number of times:
// any other error is the caller's to handle.
func TestRetrySerializableRetriesOnlySerializationFailures(t *testing.T) {
	calls := 0
	conflict := fmt.Errorf("commit: %w", &pgconn.PgError{Code: "40001"})
	err := retrySerializable(t.Context(), func() error {
		calls++
		if calls < 3 {
			return conflict
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("expected success on the third try, got %v after %d", err, calls)
	}
	calls = 0
	if err := retrySerializable(t.Context(), func() error { calls++; return conflict }); !serializationFailure(err) || calls != recordAttempts {
		t.Fatalf("expected the conflict after %d tries, got %v after %d", recordAttempts, err, calls)
	}
	calls = 0
	other := errors.New("boom")
	if err := retrySerializable(t.Context(), func() error { calls++; return other }); !errors.Is(err, other) || calls != 1 {
		t.Fatalf("a non-serialization error must not be retried, got %v after %d", err, calls)
	}
}
