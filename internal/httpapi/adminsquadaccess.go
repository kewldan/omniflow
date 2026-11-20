package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/fulfillment"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountSquadAccess registers temporary squad access on the customer card:
// an operator grants one squad for some hours, and the worker takes it back.
// Like every other subscription change it goes through fulfillment.
func (handlers *AdminHandlers) mountSquadAccess(secure chi.Router) {
	if handlers.fulfillment == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionSubscriptionsRead)).
		Get("/customers/{customerID}/subscriptions/{subscriptionID}/squad-access", handlers.listSquadAccess)
	secure.With(handlers.requirePermission(rbac.PermissionSubscriptionsWrite)).
		Post("/customers/{customerID}/subscriptions/{subscriptionID}/squad-access", handlers.grantSquadAccess)
	secure.With(handlers.requirePermission(rbac.PermissionSubscriptionsWrite)).
		Delete("/customers/{customerID}/subscriptions/{subscriptionID}/squad-access/{grantID}", handlers.revokeSquadAccess)
}

func squadGrantPayload(grant dbgen.SquadAccessGrant) map[string]any {
	return map[string]any{
		"id": uuidValue(grant.ID), "squadId": uuidValue(grant.SquadID), "source": grant.Source,
		"status": grant.Status, "startsAt": grant.StartsAt.Time, "endsAt": grant.EndsAt.Time,
	}
}

// squadAccessTarget resolves the subscription through the customer named in
// the path, as every subscription route does.
func (handlers *AdminHandlers) squadAccessTarget(writer http.ResponseWriter, request *http.Request) (customer, subscription pgtype.UUID, ok bool) {
	customerID, subscriptionID := chi.URLParam(request, "customerID"), chi.URLParam(request, "subscriptionID")
	if _, err := handlers.operations.SubscriptionTarget(request.Context(), customerID, subscriptionID); err != nil {
		handlers.operationsError(writer, request, err)
		return customer, subscription, false
	}
	if customer.Scan(customerID) != nil || subscription.Scan(subscriptionID) != nil {
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That subscription was not found")
		return customer, subscription, false
	}
	return customer, subscription, true
}

func (handlers *AdminHandlers) listSquadAccess(writer http.ResponseWriter, request *http.Request) {
	_, subscription, ok := handlers.squadAccessTarget(writer, request)
	if !ok {
		return
	}
	grants, err := handlers.fulfillment.SquadGrants(request.Context(), subscription)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	items := make([]map[string]any, 0, len(grants))
	for _, grant := range grants {
		items = append(items, squadGrantPayload(grant))
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (handlers *AdminHandlers) grantSquadAccess(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		SquadID string `json:"squadId"`
		Hours   int    `json:"hours"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	actor := actorFrom(request)
	if strings.TrimSpace(actor.Reason) == "" {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "validation_failed", "A reason is required")
		return
	}
	customer, subscription, ok := handlers.squadAccessTarget(writer, request)
	if !ok {
		return
	}
	var operator pgtype.UUID
	_ = operator.Scan(actor.AdminID)
	grant, err := handlers.fulfillment.GrantSquadAccess(request.Context(), fulfillment.SquadGrantInput{
		CustomerID: customer, SubscriptionID: subscription, SquadID: strings.TrimSpace(body.SquadID),
		Hours: body.Hours, Source: "operator", GrantedBy: operator, Reason: actor.Reason,
		// Derived from the request identifier, so a double-submitted form
		// reuses the first grant.
		IdempotencyKey: "panel-squad:" + chi.URLParam(request, "subscriptionID") + ":" + actor.RequestID,
	})
	if handlers.squadAccessError(writer, request, err) {
		return
	}
	if err = handlers.operations.RecordSubscriptionOperation(
		request.Context(), chi.URLParam(request, "customerID"), chi.URLParam(request, "subscriptionID"),
		"squad_access_granted", uuidValue(grant.ID), actor,
	); err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, squadGrantPayload(grant))
}

func (handlers *AdminHandlers) revokeSquadAccess(writer http.ResponseWriter, request *http.Request) {
	actor := actorFrom(request)
	if strings.TrimSpace(actor.Reason) == "" {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "validation_failed", "A reason is required")
		return
	}
	customer, subscription, ok := handlers.squadAccessTarget(writer, request)
	if !ok {
		return
	}
	var grantID pgtype.UUID
	if grantID.Scan(chi.URLParam(request, "grantID")) != nil {
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That grant was not found")
		return
	}
	grant, err := handlers.fulfillment.RevokeSquadAccess(request.Context(), customer, subscription, grantID)
	if handlers.squadAccessError(writer, request, err) {
		return
	}
	if err = handlers.operations.RecordSubscriptionOperation(
		request.Context(), chi.URLParam(request, "customerID"), chi.URLParam(request, "subscriptionID"),
		"squad_access_revoked", uuidValue(grant.ID), actor,
	); err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, squadGrantPayload(grant))
}

func (handlers *AdminHandlers) squadAccessError(writer http.ResponseWriter, request *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, fulfillment.ErrSquadGrantInvalid):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "squad_access_invalid", "Pick a squad and between 1 and 720 hours")
	case errors.Is(err, fulfillment.ErrSquadGrantUnavailable):
		writeProblem(writer, request, http.StatusConflict, "squad_access_unavailable", "The subscription has no running period to add a squad to, or the grant is already over")
	default:
		handlers.logger.Error("squad access change failed", "error", err)
		writeProblem(writer, request, http.StatusUnprocessableEntity, "operation_rejected", "That change could not be queued")
	}
	return true
}
