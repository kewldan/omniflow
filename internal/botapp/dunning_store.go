package botapp

import (
	"context"
	"time"
)

// PendingDunningNotice is one failed automatic charge the customer has not been
// told about.
//
// The failure code is loaded but never rendered: the customer-facing message
// says what happened to their access and what to do, and the code stays for the
// log line support reads. What the message does depend on is `Abandoned`, which
// is the difference between "we will try again" and "we have stopped".
type PendingDunningNotice struct {
	AttemptID   string
	CustomerID  string
	TelegramID  int64
	Locale      string
	Abandoned   bool
	FailureCode string
	OccurredAt  time.Time
}

// PendingDunningNotices lists failed renewal charges awaiting a message.
//
// The filter is a plain read of `notify_required`, which the renewal worker
// wrote from the rule in `internal/recurring`. Recomputing "does this failure
// deserve a message?" here would be a second copy of a product decision, free
// to drift from the first.
func (store *PostgresStore) PendingDunningNotices(
	ctx context.Context, limit int,
) ([]PendingDunningNotice, error) {
	rows, err := store.pool.Query(ctx, telegramRecipients+`
		SELECT DISTINCT ON (d.id) d.id::text, d.user_id::text, recipient.telegram_id,
		CASE WHEN COALESCE(p.locale, 'auto') = 'auto' THEN u.locale ELSE p.locale END,
		d.outcome = 'abandoned', COALESCE(d.failure_code, ''), d.occurred_at
		FROM dunning_attempts d
		JOIN users u ON u.id = d.user_id
		JOIN recipient ON recipient.user_id = d.user_id
		LEFT JOIN bot_preferences p ON p.user_id = d.user_id
		WHERE d.notify_required AND d.notified_at IS NULL AND u.status = 'active'
		ORDER BY d.id, d.occurred_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	notices := make([]PendingDunningNotice, 0, limit)
	for rows.Next() {
		var notice PendingDunningNotice
		if err := rows.Scan(&notice.AttemptID, &notice.CustomerID, &notice.TelegramID,
			&notice.Locale, &notice.Abandoned, &notice.FailureCode, &notice.OccurredAt); err != nil {
			return nil, err
		}
		notices = append(notices, notice)
	}
	return notices, rows.Err()
}

// MarkDunningNotified records that the customer was told.
//
// The `notified_at IS NULL` guard makes it safe to call twice: a second pass
// updates nothing rather than resetting the timestamp, so a retried delivery
// cannot make an old notice look fresh.
func (store *PostgresStore) MarkDunningNotified(ctx context.Context, attemptID string) error {
	_, err := store.pool.Exec(ctx,
		`UPDATE dunning_attempts SET notified_at = now()
		 WHERE id = $1::uuid AND notified_at IS NULL`, attemptID)
	return err
}
