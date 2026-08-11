package adminauthpg

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// AuditFilter is the set of optional predicates the audit browser composes from
// URL state. An empty field means "no constraint".
type AuditFilter struct {
	Category   string
	Outcome    string
	ActorType  string
	ActorID    string
	Action     string
	TargetType string
	TargetID   string
	From       *time.Time
	To         *time.Time
	Cursor     string
	PageSize   int32
	// Ascending flips the trail to oldest-first. The cursor encodes a position
	// in the ordering, so a cursor minted in one direction is not valid in the
	// other; the panel resets paging whenever the direction changes.
	Ascending bool
}

// AuditEvent is one row of the trail as the API presents it.
type AuditEvent struct {
	ID         string
	OccurredAt time.Time
	ActorType  string
	ActorID    string
	Action     string
	Category   string
	Outcome    string
	TargetType string
	TargetID   string
	Reason     string
	RequestID  string
	Metadata   map[string]any
}

// AuditPage is one keyset page of the trail.
type AuditPage struct {
	Events     []AuditEvent
	NextCursor string
}

// SearchAudit reads the append-only trail newest first.
//
// Nothing here can modify a row: the trail exposes only reads, which is what
// makes it evidence rather than editable state.
func (service *Service) SearchAudit(ctx context.Context, filter AuditFilter) (AuditPage, error) {
	pageSize := filter.PageSize
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	cursorAt, cursorID, err := decodeCursor(filter.Cursor)
	if err != nil {
		return AuditPage{}, err
	}

	// One extra row reveals whether a further page exists without counting the
	// whole table, which on an append-only trail would grow without bound.
	queries := dbgen.New(service.pool)
	var rows []dbgen.AuditEvent
	if filter.Ascending {
		rows, err = queries.SearchAuditEventsAscending(ctx, dbgen.SearchAuditEventsAscendingParams{
			CursorOccurredAt: cursorAt,
			CursorID:         cursorID,
			Category:         optionalText(filter.Category),
			Outcome:          optionalText(filter.Outcome),
			ActorType:        optionalText(filter.ActorType),
			ActorID:          optionalText(filter.ActorID),
			Action:           optionalText(filter.Action),
			TargetType:       optionalText(filter.TargetType),
			TargetID:         optionalText(filter.TargetID),
			OccurredFrom:     optionalTimestamp(filter.From),
			OccurredTo:       optionalTimestamp(filter.To),
			PageSize:         pageSize + 1,
		})
	} else {
		rows, err = queries.SearchAuditEvents(ctx, dbgen.SearchAuditEventsParams{
			CursorOccurredAt: cursorAt,
			CursorID:         cursorID,
			Category:         optionalText(filter.Category),
			Outcome:          optionalText(filter.Outcome),
			ActorType:        optionalText(filter.ActorType),
			ActorID:          optionalText(filter.ActorID),
			Action:           optionalText(filter.Action),
			TargetType:       optionalText(filter.TargetType),
			TargetID:         optionalText(filter.TargetID),
			OccurredFrom:     optionalTimestamp(filter.From),
			OccurredTo:       optionalTimestamp(filter.To),
			PageSize:         pageSize + 1,
		})
	}
	if err != nil {
		return AuditPage{}, err
	}

	page := AuditPage{Events: make([]AuditEvent, 0, len(rows))}
	if len(rows) > int(pageSize) {
		last := rows[pageSize-1]
		page.NextCursor = encodeCursor(last.OccurredAt.Time, uuidString(last.ID))
		rows = rows[:pageSize]
	}
	for _, row := range rows {
		event := AuditEvent{
			ID:         uuidString(row.ID),
			OccurredAt: row.OccurredAt.Time,
			ActorType:  row.ActorType,
			ActorID:    row.ActorID.String,
			Action:     row.Action,
			Category:   row.Category,
			Outcome:    row.Outcome,
			TargetType: row.TargetType,
			TargetID:   row.TargetID,
			Reason:     row.Reason.String,
			RequestID:  row.RequestID.String,
		}
		// Metadata is operator-facing context, never a secret: every writer in
		// this package chooses what it records. A row that fails to decode is
		// surfaced with empty metadata rather than failing the whole page.
		if len(row.Metadata) > 0 {
			_ = json.Unmarshal(row.Metadata, &event.Metadata)
		}
		page.Events = append(page.Events, event)
	}
	return page, nil
}

// AuditActions lists the distinct action names present in the trail, which
// populates the panel's action filter.
func (service *Service) AuditActions(ctx context.Context) ([]string, error) {
	return dbgen.New(service.pool).ListAuditEventActions(ctx)
}

// ErrAuditUnavailable reports that no trail is attached to record into.
var ErrAuditUnavailable = errors.New("audit trail is unavailable")

// AppendAudit records an event from outside this package, for example from an
// HTTP handler that performed an authorization denial.
//
// It opens its own transaction, so it is only correct for events that describe
// something already durable. An event that must commit with a state change
// belongs inside that change's transaction instead.
//
// An unattached service reports ErrAuditUnavailable rather than panicking, so
// a transport-layer denial still completes when no trail is wired up.
func (service *Service) AppendAudit(ctx context.Context, entry AuditEntry) error {
	if service == nil || service.pool == nil {
		return ErrAuditUnavailable
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		return appendAudit(ctx, queries, entry)
	})
}
