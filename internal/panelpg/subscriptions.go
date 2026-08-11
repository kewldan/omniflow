package panelpg

import (
	"context"
	"strings"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// SubscriptionTarget resolves a subscription to the two identifiers an operator
// action needs: the entitlement a fulfillment operation applies to, and the
// Remnawave user a device query addresses.
type SubscriptionTarget struct {
	SubscriptionID  string
	EntitlementID   string
	RemnawaveUserID int64
	Label           string
	Status          string
}

// SubscriptionTarget looks up one of a customer's subscriptions.
//
// It goes through the customer on purpose. Addressing a subscription by its own
// identifier alone would let an operator who guessed one act on a customer they
// were not looking at, and every route that reaches here already names the
// customer in its path.
func (service *Service) SubscriptionTarget(
	ctx context.Context, customerID, subscriptionID string,
) (SubscriptionTarget, error) {
	customer, err := parseUUID(customerID)
	if err != nil {
		return SubscriptionTarget{}, err
	}
	rows, err := service.queries().ListCustomerSubscriptionsDetailed(ctx, customer)
	if err != nil {
		return SubscriptionTarget{}, err
	}
	for _, row := range rows {
		if uuidString(row.Subscription.ID) != subscriptionID {
			continue
		}
		return SubscriptionTarget{
			SubscriptionID:  subscriptionID,
			EntitlementID:   uuidString(row.EntitlementID),
			RemnawaveUserID: row.Subscription.RemnawaveUserID.Int64,
			Label:           row.Subscription.Label,
			Status:          row.Subscription.Status,
		}, nil
	}
	return SubscriptionTarget{}, ErrNotFound
}

// SubscriptionOperations an operator may request from the panel.
//
// `create` and `reconcile` are deliberately absent. Provisioning is what a paid
// order triggers, and reconciliation is what the scheduled sweep does; offering
// either as a button would give an operator a way to create an entitlement with
// no order behind it, or to race the sweep.
var SubscriptionOperations = []string{
	"extend", "enable", "disable", "reset_traffic", "set_limits", "set_squads",
}

// ValidSubscriptionOperation reports whether an operator may request an
// operation.
func ValidSubscriptionOperation(operation string) bool {
	for _, allowed := range SubscriptionOperations {
		if allowed == operation {
			return true
		}
	}
	return false
}

// RecordSubscriptionOperation writes the audit event for an operator-requested
// change to a subscription.
//
// The operation itself is enqueued by the fulfillment service, which owns the
// idempotency key and the retry policy. This records who asked and why — detail
// the operation row does not carry, and the only part of it a review needs.
func (service *Service) RecordSubscriptionOperation(
	ctx context.Context, customerID, subscriptionID, operation, operationID string, actor Actor,
) error {
	if strings.TrimSpace(actor.Reason) == "" {
		return ErrValidaton
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		return appendAudit(ctx, queries, actor.audit(
			"panel.subscription."+operation, "customer", "subscription", subscriptionID,
			map[string]any{"customerId": customerID, "operationId": operationID},
		))
	})
}

// RecordDeviceRemoval writes the audit event for a device an operator removed.
//
// The hardware identifier is not recorded. It identifies a customer's physical
// device, the audit trail already names the customer and the subscription, and
// a reviewer asking "which device" is asking a question the answer to which
// would outlive the device itself.
func (service *Service) RecordDeviceRemoval(
	ctx context.Context, customerID, subscriptionID, reference string, actor Actor,
) error {
	if strings.TrimSpace(actor.Reason) == "" {
		return ErrValidaton
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		return appendAudit(ctx, queries, actor.audit(
			"panel.subscription.device_removed", "customer", "subscription", subscriptionID,
			map[string]any{"customerId": customerID, "deviceRef": reference},
		))
	})
}

// AnonymizeCustomer scrubs a customer's identifying data under the retention
// policy.
//
// It is irreversible and separate from deletion: an anonymised customer keeps
// their financial history, because an order that funded a ledger entry cannot
// be removed without unbalancing it, and loses everything that identifies who
// placed it.
func (service *Service) AnonymizeCustomer(ctx context.Context, customerID string, actor Actor) error {
	if strings.TrimSpace(actor.Reason) == "" {
		return ErrValidaton
	}
	id, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if txErr := queries.AnonymizeCustomerData(ctx, id); txErr != nil {
			return notFound(txErr)
		}
		if _, txErr := queries.InsertCustomerLifecycleEvent(ctx, dbgen.InsertCustomerLifecycleEventParams{
			UserID: id, Action: "anonymized", Reason: actor.Reason,
			ActorType: "operator", ActorID: optionalText(actor.AdminID),
			RequestID: optionalText(actor.RequestID),
		}); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.customer.anonymized", "customer", "customer", customerID, nil,
		))
	})
}

// RequestCustomerDeletion marks a customer for deletion under the retention
// policy.
//
// Deletion is a request rather than an act: the retention worker removes the
// record once its window elapses, which is what gives an operator time to
// discover a mistake and what keeps a deletion from tearing a hole in the
// ledger mid-transaction.
func (service *Service) RequestCustomerDeletion(
	ctx context.Context, customerID string, actor Actor,
) (CustomerProfile, error) {
	if strings.TrimSpace(actor.Reason) == "" {
		return CustomerProfile{}, ErrValidaton
	}
	id, err := parseUUID(customerID)
	if err != nil {
		return CustomerProfile{}, err
	}
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.ApplyCustomerLifecycle(ctx, dbgen.ApplyCustomerLifecycleParams{
			UserID: id, Status: "deleted",
		}); txErr != nil {
			return notFound(txErr)
		}
		if _, txErr := queries.InsertCustomerLifecycleEvent(ctx, dbgen.InsertCustomerLifecycleEventParams{
			UserID: id, Action: "deletion_requested", Reason: actor.Reason,
			ActorType: "operator", ActorID: optionalText(actor.AdminID),
			RequestID: optionalText(actor.RequestID),
		}); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.customer.deletion_requested", "customer", "customer", customerID, nil,
		))
	})
	if err != nil {
		return CustomerProfile{}, err
	}
	return service.CustomerProfile(ctx, customerID)
}
