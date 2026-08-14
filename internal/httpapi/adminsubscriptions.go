package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/fulfillment"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/rbac"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// mountSubscriptionOperations registers the routes that change a subscription
// or inspect its devices.
//
// They are mounted separately from the rest of the operations surface because
// they are the only panel routes that reach outside the database: one enqueues
// a Remnawave change through the fulfillment pipeline, the other reads and
// writes Remnawave directly.
func (handlers *AdminHandlers) mountSubscriptionOperations(secure chi.Router) {
	if handlers.fulfillment != nil {
		secure.With(handlers.requirePermission(rbac.PermissionSubscriptionsWrite)).
			Post("/customers/{customerID}/subscriptions/{subscriptionID}/operations",
				handlers.enqueueSubscriptionOperation)

		// Pause and resume are their own routes rather than two more values on
		// the operations endpoint. Everything there is a Remnawave change
		// described by parameters; these two are a Remnawave change *and* a
		// change to the entitlement's own clock, and the two have to commit
		// together or a customer pays for time they cannot use.
		secure.With(handlers.requirePermission(rbac.PermissionSubscriptionsWrite)).
			Post("/customers/{customerID}/subscriptions/{subscriptionID}/pause",
				handlers.pauseSubscription)
		secure.With(handlers.requirePermission(rbac.PermissionSubscriptionsWrite)).
			Post("/customers/{customerID}/subscriptions/{subscriptionID}/resume",
				handlers.resumeSubscription)
	}
	if handlers.remnawave != nil {
		secure.With(handlers.requirePermission(rbac.PermissionSubscriptionsRead)).
			Get("/customers/{customerID}/subscriptions/{subscriptionID}/devices",
				handlers.listSubscriptionDevices)
		secure.With(handlers.requirePermission(rbac.PermissionSubscriptionsWrite)).
			Delete("/customers/{customerID}/subscriptions/{subscriptionID}/devices/{deviceRef}",
				handlers.removeSubscriptionDevice)
	}
}

// enqueueSubscriptionOperation asks the fulfillment pipeline to change a
// subscription in Remnawave.
//
// It goes through the pipeline rather than calling Remnawave directly, so an
// operator change carries the same idempotency key, retry policy, drift
// detection, and history as one a purchase produced. A panel click that wrote
// to Remnawave itself would be the one change reconciliation could not explain.
func (handlers *AdminHandlers) enqueueSubscriptionOperation(
	writer http.ResponseWriter, request *http.Request,
) {
	var body struct {
		Operation    string     `json:"operation"`
		EndsAt       *time.Time `json:"endsAt"`
		TrafficBytes *int64     `json:"trafficAllowanceBytes"`
		DeviceLimit  *int32     `json:"deviceLimit"`
		SquadIDs     []string   `json:"squadIds"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	if !panelpg.ValidSubscriptionOperation(body.Operation) {
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"unsupported_operation", "That operation cannot be requested from the panel",
		)
		return
	}

	customerID := chi.URLParam(request, "customerID")
	subscriptionID := chi.URLParam(request, "subscriptionID")
	actor := actorFrom(request)

	target, err := handlers.operations.SubscriptionTarget(request.Context(), customerID, subscriptionID)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	if target.EntitlementID == "" {
		// A subscription slot with no entitlement has never been provisioned,
		// so there is nothing to extend, disable, or reset.
		writeProblem(
			writer, request, http.StatusConflict,
			"not_provisioned", "That subscription has no entitlement to change",
		)
		return
	}

	// Derived from the operator's request identifier, so a double-submitted form
	// reuses the operation the first submission made rather than enqueueing a
	// second Remnawave change.
	key := fmt.Sprintf("panel:%s:%s:%s", subscriptionID, body.Operation, actor.RequestID)
	operation, err := handlers.fulfillment.Enqueue(request.Context(), target.EntitlementID, fulfillment.OperationInput{
		Operation:             body.Operation,
		IdempotencyKey:        key,
		CorrelationID:         "panel:" + actor.RequestID,
		EndsAt:                body.EndsAt,
		TrafficAllowanceBytes: body.TrafficBytes,
		DeviceLimit:           body.DeviceLimit,
		SquadIDs:              body.SquadIDs,
	})
	if err != nil {
		handlers.logger.Error("panel subscription operation failed",
			"operation", body.Operation, "error", err)
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"operation_rejected", "That change could not be queued",
		)
		return
	}

	operationID := uuidValue(operation.ID)
	if err := handlers.operations.RecordSubscriptionOperation(
		request.Context(), customerID, subscriptionID, body.Operation, operationID, actor,
	); err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]any{
		"operationId": operationID, "operation": body.Operation, "status": operation.Status,
	})
}

// PanelDevice is one connected device as the panel shows it.
//
// The hardware identifier is deliberately absent. It is a stable identifier for
// somebody's physical device, an operator reviewing devices needs to recognise
// them rather than to name them, and a value that never reaches the browser
// cannot leak from it. `Ref` is a digest that the removal route resolves back
// to the real identifier server-side.
type PanelDevice struct {
	Ref        string    `json:"ref"`
	Platform   string    `json:"platform,omitempty"`
	Model      string    `json:"model,omitempty"`
	OSVersion  string    `json:"osVersion,omitempty"`
	LastSeenAt time.Time `json:"lastSeenAt"`
}

// deviceRef is the handle the panel uses in place of a hardware identifier.
//
// It is truncated, which is enough to address one device among a customer's few
// and short enough to be obviously not the identifier itself.
func deviceRef(hwid string) string {
	digest := sha256.Sum256([]byte(hwid))
	return hex.EncodeToString(digest[:])[:16]
}

func (handlers *AdminHandlers) listSubscriptionDevices(
	writer http.ResponseWriter, request *http.Request,
) {
	target, err := handlers.operations.SubscriptionTarget(
		request.Context(), chi.URLParam(request, "customerID"), chi.URLParam(request, "subscriptionID"),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	if target.RemnawaveUserID == 0 {
		// Never provisioned, so there is nothing connected to it.
		writeJSON(writer, http.StatusOK, map[string]any{"items": []PanelDevice{}})
		return
	}

	devices, err := handlers.remnawave.Devices(request.Context(), target.RemnawaveUserID)
	if err != nil {
		handlers.logger.Warn("panel device listing failed", "error", err)
		writeProblem(
			writer, request, http.StatusBadGateway,
			"remnawave_unavailable", "Devices could not be read from Remnawave",
		)
		return
	}

	items := make([]PanelDevice, 0, len(devices.Devices))
	for _, device := range devices.Devices {
		items = append(items, PanelDevice{
			Ref:        deviceRef(device.HWID),
			Platform:   optionalString(device.Platform),
			Model:      optionalString(device.DeviceModel),
			OSVersion:  optionalString(device.OSVersion),
			LastSeenAt: device.UpdatedAt.UTC(),
		})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

// removeSubscriptionDevice removes one device by the reference the listing
// handed out.
//
// The reference is resolved back to a hardware identifier here, by listing the
// customer's devices and matching the digest. That costs one extra Remnawave
// call and means the identifier itself never travels to the browser or back.
func (handlers *AdminHandlers) removeSubscriptionDevice(
	writer http.ResponseWriter, request *http.Request,
) {
	customerID := chi.URLParam(request, "customerID")
	subscriptionID := chi.URLParam(request, "subscriptionID")
	reference := chi.URLParam(request, "deviceRef")

	target, err := handlers.operations.SubscriptionTarget(request.Context(), customerID, subscriptionID)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	if target.RemnawaveUserID == 0 {
		writeProblem(writer, request, http.StatusNotFound, "device_not_found", "Device not found")
		return
	}

	devices, err := handlers.remnawave.Devices(request.Context(), target.RemnawaveUserID)
	if err != nil {
		writeProblem(
			writer, request, http.StatusBadGateway,
			"remnawave_unavailable", "Devices could not be read from Remnawave",
		)
		return
	}
	hwid := ""
	for _, device := range devices.Devices {
		if deviceRef(device.HWID) == reference {
			hwid = device.HWID
			break
		}
	}
	if hwid == "" {
		writeProblem(writer, request, http.StatusNotFound, "device_not_found", "Device not found")
		return
	}

	actor := actorFrom(request)
	// The audit event is written first: a removal that succeeded in Remnawave
	// and then failed to record would be a change with no trail, whereas a
	// recorded removal that then failed is visible and correctable.
	if err := handlers.operations.RecordDeviceRemoval(
		request.Context(), customerID, subscriptionID, reference, actor,
	); err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	if err := handlers.remnawave.DeleteDevice(request.Context(), target.RemnawaveUserID, hwid); err != nil {
		var apiErr *remnawave.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			// Already gone, which is the state the caller asked for.
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		writeProblem(
			writer, request, http.StatusBadGateway,
			"remnawave_unavailable", "The device could not be removed",
		)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// pauseSubscription stops a subscription without spending its remaining days.
func (handlers *AdminHandlers) pauseSubscription(writer http.ResponseWriter, request *http.Request) {
	handlers.pauseTransition(writer, request, "pause")
}

// resumeSubscription gives back exactly the time the pause took.
func (handlers *AdminHandlers) resumeSubscription(writer http.ResponseWriter, request *http.Request) {
	handlers.pauseTransition(writer, request, "resume")
}

// pauseTransition is the shared half of the two.
//
// There is no customer-facing equivalent of these routes, and that is a decision
// rather than an omission. A pause converts a dated entitlement into an
// indefinite one; letting the person holding it decide when the clock runs would
// make "thirty days" mean "thirty days of my choosing, forever". An operator
// pausing on request is a support action with a reason attached, which is what
// the audit entry records.
func (handlers *AdminHandlers) pauseTransition(
	writer http.ResponseWriter, request *http.Request, transition string,
) {
	customerID := chi.URLParam(request, "customerID")
	subscriptionID := chi.URLParam(request, "subscriptionID")
	actor := actorFrom(request)

	target, err := handlers.operations.SubscriptionTarget(request.Context(), customerID, subscriptionID)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	if target.EntitlementID == "" {
		writeProblem(
			writer, request, http.StatusConflict,
			"not_provisioned", "That subscription has no entitlement to change",
		)
		return
	}

	// Derived from the operator's request identifier, so a double-submitted
	// button reuses the first transition rather than pausing twice — which for
	// a resume would hand out the elapsed time a second time.
	key := fmt.Sprintf("panel:%s:%s:%s", subscriptionID, transition, actor.RequestID)
	correlation := "panel:" + actor.RequestID

	var operation dbgen.FulfillmentOperation
	if transition == "pause" {
		operation, err = handlers.fulfillment.Pause(request.Context(), target.EntitlementID, key, correlation)
	} else {
		operation, err = handlers.fulfillment.Resume(request.Context(), target.EntitlementID, key, correlation)
	}
	if errors.Is(err, fulfillment.ErrNotPausable) {
		// 409 rather than 422: the request is well formed and the subscription
		// is simply not in a state it applies to, which is something that can
		// change between the screen loading and the button being pressed.
		writeProblem(
			writer, request, http.StatusConflict, "not_pausable",
			"That subscription is not in a state that can be "+transition+"d",
		)
		return
	}
	if err != nil {
		handlers.logger.Error("panel subscription pause failed",
			"transition", transition, "error", err)
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"operation_rejected", "That change could not be queued",
		)
		return
	}

	operationID := uuidValue(operation.ID)
	if err := handlers.operations.RecordSubscriptionOperation(
		request.Context(), customerID, subscriptionID, transition, operationID, actor,
	); err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]any{
		"operationId": operationID, "operation": transition, "status": operation.Status,
	})
}
