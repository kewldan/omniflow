package panelpg

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// CloseAllSupportTickets closes every open or pending ticket, of one queue
// when queueID is set. It needs a reason, because emptying the desk is the
// kind of act a review asks about.
func (service *Service) CloseAllSupportTickets(ctx context.Context, queueID string, actor Actor) (int64, error) {
	if strings.TrimSpace(actor.Reason) == "" {
		return 0, ErrValidaton
	}
	queue := pgtype.UUID{}
	if queueID != "" {
		parsed, err := parseUUID(queueID)
		if err != nil {
			return 0, err
		}
		queue = parsed
	}
	var closed int64
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		var err error
		if closed, err = queries.CloseAllSupportTickets(ctx, queue); err != nil {
			return err
		}
		return appendAudit(ctx, queries, actor.audit("support.tickets.closed_all", "support", "support_queue",
			orAll(queueID), map[string]any{"closed": closed}))
	})
	return closed, err
}

// DeleteSupportMessage takes one message out of the customer's view. The row
// stays, marked, for the desk and the audit trail; an operator reply not yet
// pushed to Telegram is never pushed.
func (service *Service) DeleteSupportMessage(ctx context.Context, ticketID string, messageID int64, actor Actor) error {
	ticket, err := parseUUID(ticketID)
	if err != nil {
		return err
	}
	deletedBy := pgtype.UUID{}
	if actor.AdminID != "" {
		deletedBy, _ = parseUUID(actor.AdminID)
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		removed, err := queries.DeleteSupportMessage(ctx, dbgen.DeleteSupportMessageParams{
			MessageID: messageID, TicketID: ticket, DeletedBy: deletedBy,
		})
		if err != nil {
			return err
		}
		if removed == 0 {
			return ErrNotFound
		}
		return appendAudit(ctx, queries, actor.audit("support.message.deleted", "support", "support_ticket", ticketID,
			map[string]any{"messageId": messageID}))
	})
}

func orAll(value string) string {
	if value == "" {
		return "*"
	}
	return value
}
