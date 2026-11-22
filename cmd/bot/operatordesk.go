package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/botapp"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// operatorDesk is what the operator group's notice buttons act through: the
// panel's own service, with the Telegram account as the actor. A close or a
// block from the chat is therefore the same change, the same audit entry, and
// the same customer message as one made in the panel.
type operatorDesk struct {
	pool  *pgxpool.Pool
	panel *panelpg.Service
}

var _ botapp.OperatorDesk = operatorDesk{}

// deskReason is the reason the audit trail records for a notice-button action.
const deskReason = "from a notice in the operator group in Telegram"

func deskActor(actor string) panelpg.Actor {
	return panelpg.Actor{Type: "operator", AdminID: actor, Reason: deskReason}
}

func (desk operatorDesk) TicketStatus(ctx context.Context, ticketID string) (string, error) {
	var status string
	err := desk.pool.QueryRow(ctx, `SELECT status FROM support_tickets WHERE id = $1::uuid`, ticketID).Scan(&status)
	return status, err
}

func (desk operatorDesk) CloseTicket(ctx context.Context, ticketID, actor string) error {
	_, err := desk.panel.SetTicketStatus(ctx, ticketID, "closed", deskActor(actor))
	return err
}

func (desk operatorDesk) CustomerStatus(ctx context.Context, customerID string) (string, error) {
	var status string
	err := desk.pool.QueryRow(ctx, `SELECT status FROM users WHERE id = $1::uuid`, customerID).Scan(&status)
	return status, err
}

func (desk operatorDesk) SetCustomerBlocked(ctx context.Context, customerID string, blocked bool, actor string) error {
	status := "active"
	if blocked {
		status = "suspended"
	}
	_, err := desk.panel.SetCustomerStatus(ctx, customerID, status, deskActor(actor))
	return err
}

func (desk operatorDesk) RetryDelivery(ctx context.Context, operationID, actor string) error {
	_, err := desk.panel.RetryFulfillment(ctx, operationID, deskActor(actor))
	if errors.Is(err, panelpg.ErrRejected) {
		return botapp.ErrNothingToRetry
	}
	return err
}

// RecordDenied audits a tap from somebody who may not act in the group.
func (desk operatorDesk) RecordDenied(ctx context.Context, actor, action, targetID string) error {
	metadata, err := json.Marshal(map[string]any{"channel": "telegram_operator_group", "action": action})
	if err != nil {
		return err
	}
	_, err = desk.pool.Exec(ctx, `INSERT INTO audit_events
		(actor_type, actor_id, action, category, outcome, target_type, target_id, reason, metadata)
		VALUES ('operator', $1, 'telegram.notice_action.denied', 'authorization', 'denied',
			'operator_notice', $2, $3, $4)`, actor, targetID, deskReason, metadata)
	return err
}
