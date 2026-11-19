package commercepg

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// serializableAttempts bounds how often a Serializable transaction is retried
// after PostgreSQL aborted it to keep the history serializable.
const serializableAttempts = 8

// Retry pauses grow from serializableBaseDelay and are capped at
// serializableMaxDelay, with full jitter. Retrying immediately made every
// loser of a contended row (eight people claiming one gift code at once)
// collide again on the same row, exhausting its attempts with a raw 40001
// instead of reaching the refusal the next attempt would have produced.
const (
	serializableBaseDelay = 5 * time.Millisecond
	serializableMaxDelay  = 250 * time.Millisecond
)

// retrySerializable runs a whole transaction again when it failed with a
// serialization failure or a deadlock. The function must be a complete
// transaction with no effect outside the database, so a rolled-back attempt
// leaves nothing behind.
func retrySerializable(ctx context.Context, attempt func() error) error {
	for try := 1; ; try++ {
		err := attempt()
		if err == nil || try >= serializableAttempts || !serializationFailure(err) || ctx.Err() != nil {
			return err
		}
		timer := time.NewTimer(serializableBackoff(try))
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
	}
}

// serializableBackoff is the pause before retry number try+1: a uniformly
// random duration up to an exponentially growing ceiling.
func serializableBackoff(try int) time.Duration {
	ceiling := min(serializableBaseDelay<<min(try-1, 10), serializableMaxDelay)
	return time.Duration(rand.Int64N(int64(ceiling)) + 1)
}

func serializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01")
}
