package commercepg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestRetrySerializableRetriesOnlySerializationFailures(t *testing.T) {
	t.Parallel()
	conflict := &pgconn.PgError{Code: "40001"}
	calls := 0
	err := retrySerializable(context.Background(), func() error {
		calls++
		if calls < 3 {
			return conflict
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("err=%v calls=%d, want nil after 3 attempts", err, calls)
	}

	calls = 0
	other := errors.New("not a conflict")
	if err := retrySerializable(context.Background(), func() error { calls++; return other }); !errors.Is(err, other) || calls != 1 {
		t.Fatalf("non-serialization error retried: err=%v calls=%d", err, calls)
	}
}

func TestRetrySerializableGivesUpAfterTheBound(t *testing.T) {
	t.Parallel()
	calls := 0
	err := retrySerializable(context.Background(), func() error { calls++; return &pgconn.PgError{Code: "40P01"} })
	if !serializationFailure(err) || calls != serializableAttempts {
		t.Fatalf("err=%v calls=%d, want deadlock after %d attempts", err, calls, serializableAttempts)
	}
}

func TestRetrySerializableStopsWhenTheContextEnds(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := retrySerializable(ctx, func() error {
		calls++
		cancel()
		return &pgconn.PgError{Code: "40001"}
	})
	if !serializationFailure(err) || calls != 1 {
		t.Fatalf("err=%v calls=%d, want one attempt once the context ended", err, calls)
	}
}

func TestSerializableBackoffIsBoundedAndPositive(t *testing.T) {
	t.Parallel()
	for try := 1; try <= 40; try++ {
		delay := serializableBackoff(try)
		if delay <= 0 || delay > serializableMaxDelay {
			t.Fatalf("try %d: delay %v outside (0, %v]", try, delay, serializableMaxDelay)
		}
	}
	if serializableBackoff(1) > serializableBaseDelay+time.Nanosecond {
		t.Fatal("first pause exceeds the base delay")
	}
}
