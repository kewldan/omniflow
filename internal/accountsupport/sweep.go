package accountsupport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5"
)

// Remover is the optional half of AttachmentStore that retention needs.
//
// It is separate from AttachmentStore because serving an attachment and
// reclaiming its disk are different privileges: the download path holds a store
// it can only read from, and nothing on a customer request can reach a delete.
type Remover interface {
	// Remove deletes the content under a key. A key that is already gone is not
	// an error: retention runs repeatedly and a second pass over the same file
	// must be a no-op rather than a failure.
	Remove(ctx context.Context, key string) error
}

// Remove deletes one stored file.
func (store DirectoryStore) Remove(ctx context.Context, key string) error {
	target, err := store.path(key)
	if err != nil {
		return err
	}
	if err = os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrAttachmentStorage, err)
	}
	// The fan-out directory is removed when it empties. RemoveAll would be wrong
	// here: os.Remove refuses a directory that still holds files, which is
	// exactly the check wanted.
	_ = os.Remove(filepath.Dir(target))
	return nil
}

// SweepExpiredAttachments deletes attachment rows past their retention window
// and removes the files that no surviving row still references.
//
// The two halves have to happen in this order and with that condition. Files are
// named by their content digest, so two customers who uploaded identical bytes
// share one file: deleting it because one row expired would break the other
// customer's download. The reference check is therefore against the rows that
// remain after the delete, not against the rows being deleted.
//
// A file that cannot be removed is logged and skipped rather than failing the
// sweep. The row is already gone, which is what the retention promise is about;
// an unreclaimed byte on disk is a housekeeping problem, and stopping the sweep
// over one would leave every later row unretained too.
func (service *Service) SweepExpiredAttachments(ctx context.Context) (rows int64, files int64, err error) {
	remover, canRemove := service.attachments.(Remover)

	transaction, err := service.pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	// The keys are read inside the transaction that deletes the rows, so a row
	// written between the read and the delete is not silently orphaned.
	candidates, err := collectKeys(ctx, transaction, `SELECT DISTINCT storage_key
		FROM support_attachments
		WHERE retain_until <= now() AND storage_key IS NOT NULL`)
	if err != nil {
		return 0, 0, err
	}

	tag, err := transaction.Exec(ctx, `DELETE FROM support_attachments WHERE retain_until <= now()`)
	if err != nil {
		return 0, 0, err
	}
	rows = tag.RowsAffected()

	// Whatever is still referenced after the delete must keep its bytes.
	var orphans []string
	if canRemove && len(candidates) > 0 {
		survivors, keysErr := collectKeys(ctx, transaction, `SELECT DISTINCT storage_key
			FROM support_attachments WHERE storage_key = ANY($1)`, candidates)
		if keysErr != nil {
			return 0, 0, keysErr
		}
		held := make(map[string]struct{}, len(survivors))
		for _, key := range survivors {
			held[key] = struct{}{}
		}
		for _, key := range candidates {
			if _, still := held[key]; !still {
				orphans = append(orphans, key)
			}
		}
	}

	if err = transaction.Commit(ctx); err != nil {
		return 0, 0, err
	}

	// Files are removed only after the commit. Doing it inside the transaction
	// would delete bytes a rollback then decided to keep, and a file is the one
	// thing a rollback cannot restore.
	for _, key := range orphans {
		if removeErr := remover.Remove(ctx, key); removeErr != nil {
			service.logger.Warn(
				"a retained support attachment file could not be removed", "error", removeErr,
			)
			continue
		}
		files++
	}
	return rows, files, nil
}

// collectKeys reads a single text column into a slice.
//
// Every query it serves restricts to rows with a storage key, so nothing here
// has to filter out a Telegram reference — the column only ever holds a key to
// something this installation is actually holding.
func collectKeys(ctx context.Context, transaction pgx.Tx, query string, args ...any) ([]string, error) {
	result, err := transaction.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer result.Close()
	keys := make([]string, 0, 16)
	for result.Next() {
		var key string
		if err = result.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, result.Err()
}
