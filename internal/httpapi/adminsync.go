package httpapi

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/fulfillment"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountSubscriptionSync registers the customer card's "Sync with Remnawave":
// a read that compares the subscription's entitlement with its Remnawave user
// field by field, and a write that pushes Omniflow's state through the
// fulfillment pipeline once the operator has seen the difference.
//
// The write never touches Remnawave itself (decision 0004). It queues a
// reconcile, which pushes the expiry, limits, and squads and records the
// drift it found, and — when the two sides disagree on whether the user is
// switched on — an enable or disable carrying Omniflow's status.
func (handlers *AdminHandlers) mountSubscriptionSync(secure chi.Router) {
	if handlers.fulfillment == nil || handlers.remnawave == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionSubscriptionsRead)).
		Get("/customers/{customerID}/subscriptions/{subscriptionID}/sync", handlers.previewSubscriptionSync)
	secure.With(handlers.requirePermission(rbac.PermissionSubscriptionsWrite)).
		Post("/customers/{customerID}/subscriptions/{subscriptionID}/sync", handlers.applySubscriptionSync)
}

// syncTarget resolves the subscription and reads the diff, answering the
// request itself when either fails.
func (handlers *AdminHandlers) syncTarget(
	writer http.ResponseWriter, request *http.Request,
) (string, fulfillment.SyncPreview, bool) {
	target, err := handlers.operations.SubscriptionTarget(
		request.Context(), chi.URLParam(request, "customerID"), chi.URLParam(request, "subscriptionID"),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return "", fulfillment.SyncPreview{}, false
	}
	if target.EntitlementID == "" {
		writeProblem(
			writer, request, http.StatusConflict,
			"not_provisioned", "That subscription has no entitlement to compare",
		)
		return "", fulfillment.SyncPreview{}, false
	}
	preview, err := handlers.fulfillment.PreviewSync(request.Context(), target.EntitlementID, handlers.remnawave)
	if err != nil {
		handlers.logger.Warn("panel subscription sync preview failed", "error", err)
		writeProblem(
			writer, request, http.StatusBadGateway,
			"remnawave_unavailable", "The subscription could not be compared with Remnawave",
		)
		return "", fulfillment.SyncPreview{}, false
	}
	return target.EntitlementID, preview, true
}

func (handlers *AdminHandlers) previewSubscriptionSync(writer http.ResponseWriter, request *http.Request) {
	if _, preview, ok := handlers.syncTarget(writer, request); ok {
		writeJSON(writer, http.StatusOK, preview)
	}
}

// applySubscriptionSync queues Omniflow's state for the subscription's
// Remnawave user. The operations are keyed by the operator's request, so a
// double-submitted confirmation reuses what the first one queued.
func (handlers *AdminHandlers) applySubscriptionSync(writer http.ResponseWriter, request *http.Request) {
	customerID := chi.URLParam(request, "customerID")
	subscriptionID := chi.URLParam(request, "subscriptionID")
	actor := actorFrom(request)
	if actor.Reason == "" {
		// Checked before anything is queued, so a refused audit entry can
		// never leave a Remnawave change behind it.
		handlers.operationsError(writer, request, panelpg.ErrValidaton)
		return
	}
	entitlementID, preview, ok := handlers.syncTarget(writer, request)
	if !ok {
		return
	}
	operations := []string{"reconcile"}
	if followUp := fulfillment.SyncFollowUp(preview); followUp != "" {
		operations = append(operations, followUp)
	}
	queued := make([]map[string]any, 0, len(operations))
	for _, name := range operations {
		operation, err := handlers.fulfillment.Enqueue(request.Context(), entitlementID, fulfillment.OperationInput{
			Operation:      name,
			IdempotencyKey: fmt.Sprintf("panel-sync:%s:%s:%s", subscriptionID, name, actor.RequestID),
			CorrelationID:  "panel-sync:" + actor.RequestID,
		})
		if err != nil {
			handlers.logger.Error("panel subscription sync failed", "operation", name, "error", err)
			writeProblem(
				writer, request, http.StatusUnprocessableEntity,
				"operation_rejected", "The sync could not be queued",
			)
			return
		}
		operationID := uuidValue(operation.ID)
		if err := handlers.operations.RecordSubscriptionOperation(
			request.Context(), customerID, subscriptionID, "sync_"+name, operationID, actor,
		); err != nil {
			handlers.operationsError(writer, request, err)
			return
		}
		queued = append(queued, map[string]any{
			"operationId": operationID, "operation": name, "status": operation.Status,
		})
	}
	writeJSON(writer, http.StatusAccepted, map[string]any{"operations": queued, "preview": preview})
}
