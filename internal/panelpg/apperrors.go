package panelpg

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// AppError is one grouped application error from the error log.
type AppError struct {
	ID          string     `json:"id"`
	Process     string     `json:"process"`
	Message     string     `json:"message"`
	Source      string     `json:"source,omitempty"`
	Fingerprint string     `json:"fingerprint"`
	Occurrences int64      `json:"occurrences"`
	FirstSeenAt time.Time  `json:"firstSeenAt"`
	LastSeenAt  time.Time  `json:"lastSeenAt"`
	ResolvedAt  *time.Time `json:"resolvedAt,omitempty"`
}

// AppErrors lists the error log, open errors only unless asked otherwise.
func (service *Service) AppErrors(ctx context.Context, process string, includeResolved bool) ([]AppError, error) {
	rows, err := service.queries().ListAppErrors(ctx, dbgen.ListAppErrorsParams{
		IncludeResolved: includeResolved,
		Process:         pgtype.Text{String: process, Valid: process != ""},
		PageSize:        200,
	})
	if err != nil {
		return nil, err
	}
	out := make([]AppError, 0, len(rows))
	for _, row := range rows {
		out = append(out, appErrorFrom(row))
	}
	return out, nil
}

// ResolveAppError marks one error handled. It reopens by itself if the error
// recurs, which is why resolving is cheap and needs no reason.
func (service *Service) ResolveAppError(ctx context.Context, errorID string, actor Actor) (AppError, error) {
	id, err := parseUUID(errorID)
	if err != nil {
		return AppError{}, err
	}
	var resolved AppError
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, resolveErr := queries.ResolveAppError(ctx, id)
		if resolveErr != nil {
			return notFound(resolveErr)
		}
		resolved = appErrorFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"system.error.resolved", "system", "app_error", errorID,
			map[string]any{"process": row.Process, "occurrences": row.Occurrences}))
	})
	return resolved, err
}

// ResolveAllAppErrors clears the open error log.
func (service *Service) ResolveAllAppErrors(ctx context.Context, actor Actor) (int64, error) {
	var count int64
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		var resolveErr error
		if count, resolveErr = queries.ResolveAllAppErrors(ctx); resolveErr != nil {
			return resolveErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"system.error.resolved_all", "system", "app_error", "*", map[string]any{"count": count}))
	})
	return count, err
}

func appErrorFrom(row dbgen.AppError) AppError {
	return AppError{
		ID: uuidString(row.ID), Process: row.Process, Message: row.Message, Source: row.Source,
		Fingerprint: row.Fingerprint[:12], Occurrences: row.Occurrences,
		FirstSeenAt: row.FirstSeenAt.Time, LastSeenAt: row.LastSeenAt.Time,
		ResolvedAt: timePointer(row.ResolvedAt),
	}
}
