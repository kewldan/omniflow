package analytics

import (
	"context"
	"time"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

const (
	// SnapshotBackfillDays is how far back the first snapshot pass
	// reconstructs the base from the entitlement history, so a new
	// installation — or one just migrated from the previous bot — has a year
	// of trend on its first day rather than one point.
	SnapshotBackfillDays = 365
	// snapshotBackfillPerRun bounds one pass, so a year of history arrives
	// over a few passes instead of holding a connection for minutes.
	snapshotBackfillPerRun = 62
)

// TakeSnapshots records today's subscription base, replacing the earlier
// reading for today, and fills in up to snapshotBackfillPerRun missing days
// from the last SnapshotBackfillDays.
//
// Days are UTC calendar days, read at the end of the day; today is read as of
// now. A past day is reconstructed from the entitlement history, which is
// exact except where a refund or a pause has since moved an entitlement's end
// — the reason the snapshot is kept at all, rather than recomputed every time.
func (service *Service) TakeSnapshots(ctx context.Context) (int, error) {
	now := service.now()
	today := dateOf(now)
	if err := service.snapshot(ctx, today, now); err != nil {
		return 0, err
	}
	taken := 1

	from := today.AddDate(0, 0, -SnapshotBackfillDays)
	existing, err := service.queries().SubscriptionSnapshotDaysTaken(ctx, dbgen.SubscriptionSnapshotDaysTakenParams{
		FromDay: date(from), ToDay: date(today),
	})
	if err != nil {
		return taken, err
	}
	have := make(map[string]bool, len(existing))
	for _, day := range existing {
		have[dayString(day)] = true
	}
	// Newest first: the recent past is what an operator looks at.
	for day := today.AddDate(0, 0, -1); !day.Before(from) && taken <= snapshotBackfillPerRun; day = day.AddDate(0, 0, -1) {
		if have[day.Format(time.DateOnly)] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return taken, err
		}
		if err := service.snapshot(ctx, day, day.AddDate(0, 0, 1).Add(-time.Second)); err != nil {
			return taken, err
		}
		taken++
	}
	return taken, nil
}

// snapshot replaces one day's rows with the base as it stood at the instant.
func (service *Service) snapshot(ctx context.Context, day, at time.Time) error {
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if err := queries.DeleteSubscriptionSnapshot(ctx, date(day)); err != nil {
			return err
		}
		if _, err := queries.InsertSubscriptionSnapshot(ctx, dbgen.InsertSubscriptionSnapshotParams{
			Day: date(day), At: timestamp(at),
		}); err != nil {
			return err
		}
		return queries.MarkSubscriptionSnapshotDay(ctx, date(day))
	})
}
