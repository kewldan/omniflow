// Package analytics answers the business questions the dashboard cannot:
// how the paying base moved over time, how much of it renews, what a cohort is
// worth, what acquiring a customer cost, how far visitors get before paying,
// and how satisfied customers are with support.
//
// It keeps three kinds of history that nothing else records — a daily
// snapshot of the subscription base, hourly node traffic samples, and the
// first time a customer reached each funnel step — and reads everything else
// from the commerce tables as they are.
//
// The rules of the sales report hold here too. Provider money and wallet
// credit are never added together, money is reported per currency and never
// converted, and a figure with nothing behind it is absent rather than zero.
package analytics

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// MaxWindow is the longest period a report covers, the same bound the sales
// report uses.
const MaxWindow = 2 * 366 * 24 * time.Hour

var (
	// ErrInvalid marks a request the caller can correct.
	ErrInvalid = errors.New("analytics: invalid request")
	// ErrNotFound marks a record that does not exist.
	ErrNotFound = errors.New("analytics: not found")
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Service reads and records business analytics.
type Service struct {
	pool  *pgxpool.Pool
	clock func() time.Time
}

// New builds the service.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, clock: time.Now}
}

// WithClock replaces the clock, for tests.
func (service *Service) WithClock(clock func() time.Time) *Service {
	service.clock = clock
	return service
}

func (service *Service) now() time.Time { return service.clock().UTC() }

func (service *Service) queries() *dbgen.Queries { return dbgen.New(service.pool) }

func (service *Service) inTx(ctx context.Context, body func(*dbgen.Queries) error) error {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := body(dbgen.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Period is a validated reporting window.
type Period struct {
	Since    time.Time
	Until    time.Time
	Timezone string
	location *time.Location
}

// NewPeriod validates a window and its timezone.
func NewPeriod(since, until time.Time, timezone string) (Period, error) {
	if !until.After(since) {
		return Period{}, invalid("the period ends before it starts")
	}
	if until.Sub(since) > MaxWindow {
		return Period{}, invalid("a report covers at most %d days", int(MaxWindow.Hours()/24))
	}
	if timezone == "" {
		timezone = "UTC"
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return Period{}, invalid("%q is not a known timezone", timezone)
	}
	return Period{Since: since.UTC(), Until: until.UTC(), Timezone: timezone, location: location}, nil
}

// days returns the first and last calendar day the period touches, in its
// timezone.
func (period Period) days() (time.Time, time.Time) {
	first := period.Since.In(period.location)
	last := period.Until.Add(-time.Nanosecond).In(period.location)
	return dateOf(first), dateOf(last)
}

func dateOf(moment time.Time) time.Time {
	return time.Date(moment.Year(), moment.Month(), moment.Day(), 0, 0, 0, 0, time.UTC)
}

func timestamp(moment time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: moment.UTC(), Valid: true}
}

func date(day time.Time) pgtype.Date {
	return pgtype.Date{Time: dateOf(day), Valid: true}
}

func dayString(value pgtype.Date) string {
	if !value.Valid {
		return ""
	}
	return value.Time.Format(time.DateOnly)
}

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", value.Bytes[0:4], value.Bytes[4:6], value.Bytes[6:8], value.Bytes[8:10], value.Bytes[10:16])
}

func parseUUID(value string) (pgtype.UUID, error) {
	var parsed pgtype.UUID
	if err := parsed.Scan(value); err != nil {
		return pgtype.UUID{}, invalid("%q is not an identifier", value)
	}
	return parsed, nil
}

// rate is numerator over denominator, or nil when there is nothing to divide
// by: "nobody was eligible" and "nobody did it" are different facts, and a
// screen that rendered the first as 0% would send somebody looking for a
// problem that is not there.
func rate(numerator, denominator int64) *float64 {
	if denominator <= 0 {
		return nil
	}
	value := float64(numerator) / float64(denominator)
	return &value
}

// perUnit divides an amount by a count, or returns nil for no count.
func perUnit(amount, count int64) *int64 {
	if count <= 0 {
		return nil
	}
	value := amount / count
	return &value
}

// Money is an amount in one currency, in minor units.
type Money struct {
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amountMinor"`
}
