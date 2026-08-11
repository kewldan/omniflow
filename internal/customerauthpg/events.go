package customerauthpg

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// SecurityEvent is one entry in the log the customer reads about their own
// account.
type SecurityEvent struct {
	ID         string
	Event      string
	IP         string
	UserAgent  string
	Metadata   map[string]any
	OccurredAt time.Time
}

// appendSecurityEvent writes one entry inside the caller's transaction, so the
// log commits with the change it describes and can never announce something that
// was rolled back.
//
// The `event` vocabulary is closed by the table's own check constraint, and the
// metadata this package passes is limited to values that are already the
// customer's own — an auth method, a provider slug, a count. Nothing here ever
// carries another party's identifier, a token, a subscription link, or an
// amount.
func (service *Service) appendSecurityEvent(
	ctx context.Context, queries *dbgen.Queries,
	customerID, event string, request RequestContext, metadata map[string]any,
) error {
	userID, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	encoded, err := encodeMetadata(metadata)
	if err != nil {
		return err
	}
	_, err = queries.InsertCustomerSecurityEvent(ctx, dbgen.InsertCustomerSecurityEventParams{
		UserID: userID, Event: event,
		Ip: request.IP, UserAgent: optionalText(request.UserAgent),
		RequestID: optionalText(request.RequestID), Metadata: encoded,
	})
	return err
}

// RecordSecurityEvent appends an entry from outside this package.
//
// Rotating a subscription link and removing devices happen in the fulfillment
// and subscription services, but they are exactly the events a customer looks
// for when something seems wrong, so those services record them here rather than
// leaving the log to describe sign-ins alone.
func (service *Service) RecordSecurityEvent(
	ctx context.Context, customerID, event string, request RequestContext, metadata map[string]any,
) error {
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		return service.appendSecurityEvent(ctx, queries, customerID, event, request, metadata)
	})
}

// ListSecurityEvents returns the customer's log, newest first.
func (service *Service) ListSecurityEvents(
	ctx context.Context, customerID string, cursor *time.Time, cursorID string, limit int32,
) ([]SecurityEvent, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	params := dbgen.ListCustomerSecurityEventsParams{UserID: userID, PageSize: limit}
	if cursor != nil && cursorID != "" {
		params.CursorOccurredAt = pgtype.Timestamptz{Time: cursor.UTC(), Valid: true}
		if params.CursorID, err = parseUUID(cursorID); err != nil {
			return nil, err
		}
	}

	rows, err := dbgen.New(service.pool).ListCustomerSecurityEvents(ctx, params)
	if err != nil {
		return nil, err
	}
	events := make([]SecurityEvent, 0, len(rows))
	for _, row := range rows {
		events = append(events, SecurityEvent{
			ID:         uuidString(row.ID),
			Event:      row.Event,
			IP:         addressString(row.Ip),
			UserAgent:  row.UserAgent.String,
			Metadata:   decodeMetadata(row.Metadata),
			OccurredAt: row.OccurredAt.Time.UTC(),
		})
	}
	return events, nil
}
