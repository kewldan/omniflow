package commercepg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// serializableAttempts bounds how often a Serializable transaction is retried
// after PostgreSQL aborted it to keep the history serializable.
const serializableAttempts = 5

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
	}
}

func serializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01")
}
