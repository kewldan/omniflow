package channelworker

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/fulfillment"
)

// Enforcer carries out the consequence of a membership decision.
//
// The worker decides; the enforcer acts. Keeping them apart is what lets the
// decision be unit-tested without Remnawave and lets an installation run the
// worker with no enforcer at all, which records and warns but takes nothing
// away.
type Enforcer interface {
	// Suspend takes access away from every live entitlement the customer
	// holds. It is called once per lapse, after the grace period has run out.
	Suspend(ctx context.Context, customerID pgtype.UUID, at time.Time) error
	// Restore gives access back after a rejoin. It is called once per
	// recovery, and only for a customer the worker itself suspended.
	Restore(ctx context.Context, customerID pgtype.UUID, at time.Time) error
}

// ChannelEvent is what the customer is told about.
type ChannelEvent struct {
	CustomerID string
	TelegramID int64
	// Kind is one of EventWarned, EventSuspended, EventRestored.
	Kind string
	// GraceUntil is set on a warning: the moment access is taken away unless
	// the customer rejoins before it.
	GraceUntil *time.Time
	// Missing lists the channels the customer has to join, each with the link
	// that satisfies it. A warning that does not say which channel cannot be
	// acted on.
	Missing []MissingChannel
}

// MissingChannel is one channel the customer has to join.
type MissingChannel struct {
	Title     string
	InviteURL string
}

// Event kinds.
const (
	EventWarned    = "warned"
	EventSuspended = "suspended"
	EventRestored  = "restored"
)

// Notifier tells the customer what the worker decided. A nil notifier keeps
// the decision in the database only.
type Notifier interface {
	NotifyChannelEvent(ctx context.Context, event ChannelEvent) error
}

// FulfillmentEnforcer suspends and restores through the ordinary fulfillment
// pipeline, so a customer suspended for leaving a channel carries the same
// operation history as one disabled by an operator, and a rejoin is an
// `enable` any operator can read in the same place.
type FulfillmentEnforcer struct {
	pool    *pgxpool.Pool
	service *fulfillment.Service
}

// NewFulfillmentEnforcer builds the enforcer against the fulfillment service of
// the process that runs the worker.
func NewFulfillmentEnforcer(pool *pgxpool.Pool, service *fulfillment.Service) *FulfillmentEnforcer {
	return &FulfillmentEnforcer{pool: pool, service: service}
}

// Suspend enqueues a `disable` for every live entitlement.
func (enforcer *FulfillmentEnforcer) Suspend(ctx context.Context, customerID pgtype.UUID, at time.Time) error {
	return enforcer.apply(ctx, customerID, "disable", at)
}

// Restore enqueues an `enable` for every live entitlement Remnawave reports
// disabled. An entitlement that is already active needs nothing, and one an
// operator paused is not listed at all.
func (enforcer *FulfillmentEnforcer) Restore(ctx context.Context, customerID pgtype.UUID, at time.Time) error {
	return enforcer.apply(ctx, customerID, "enable", at)
}

func (enforcer *FulfillmentEnforcer) apply(ctx context.Context, customerID pgtype.UUID, operation string, at time.Time) error {
	if enforcer == nil || enforcer.service == nil {
		return nil
	}
	entitlements, err := dbgen.New(enforcer.pool).ListEntitlementsForChannelEnforcement(ctx, customerID)
	if err != nil {
		return err
	}
	for _, entitlement := range entitlements {
		if operation == "enable" && entitlement.Status != "disabled" {
			continue
		}
		id := uuidString(entitlement.ID)
		// The key names the entitlement and the moment of the decision, so a
		// worker pass that retries the same decision reaches the same
		// operation, while a later lapse after a rejoin is a new one.
		key := fmt.Sprintf("channel:%s:%s:%d", operation, id, at.Unix())
		if _, err := enforcer.service.Enqueue(ctx, id, fulfillment.OperationInput{
			Operation: operation, IdempotencyKey: key, CorrelationID: "channel-gate:" + key,
		}); err != nil {
			return fmt.Errorf("%s entitlement %s: %w", operation, id, err)
		}
	}
	return nil
}
