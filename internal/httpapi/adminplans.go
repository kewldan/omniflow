package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/rbac"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// mountPlanOptions registers the routes that help an operator author plans
// against the live Remnawave panel.
//
// Reading squads needs `catalog.read`: the list exists to fill the plan
// editor's pickers, and an operator who cannot read the catalogue has no use
// for it.
func (handlers *AdminHandlers) mountPlanOptions(secure chi.Router) {
	if handlers.operations == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionCatalogRead)).
		Get("/remnawave/squads", handlers.remnawaveSquads)
	secure.With(handlers.requirePermission(rbac.PermissionCatalogWrite)).
		Put("/catalog/plans/{planID}/options", handlers.savePlanOptions)
	secure.With(handlers.requirePermission(rbac.PermissionCatalogRead)).
		Get("/catalog/simple-subscription", handlers.simpleSubscription)
	secure.With(handlers.requirePermission(rbac.PermissionCatalogWrite)).
		Put("/catalog/simple-subscription", handlers.saveSimpleSubscription)
	// Moving live subscriptions onto new squads changes what customers can
	// reach, so it needs the subscription permission, not only the catalogue.
	if handlers.fulfillment != nil {
		secure.With(handlers.requirePermission(rbac.PermissionSubscriptionsWrite)).
			Post("/catalog/plans/{planID}/apply-squads", handlers.applyPlanSquads)
	}
	secure.With(handlers.requirePermission(rbac.PermissionSettingsRead)).
		Get("/settings/subscription-policy", handlers.subscriptionPolicy)
	secure.With(handlers.requirePermission(rbac.PermissionSettingsWrite)).
		Put("/settings/subscription-policy", handlers.saveSubscriptionPolicy)
}

func (handlers *AdminHandlers) savePlanOptions(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.PlanOptions
	if !decodeJSON(writer, request, &body) {
		return
	}
	options, err := handlers.operations.SavePlanOptions(
		request.Context(), chi.URLParam(request, "planID"), body, actorFrom(request),
	)
	handlers.respond(writer, request, options, err)
}

// applyPlanSquads moves every live subscription on a plan onto the squads of
// its newest version. A reason is required: this changes what every customer
// on the plan can reach.
func (handlers *AdminHandlers) applyPlanSquads(writer http.ResponseWriter, request *http.Request) {
	actor := actorFrom(request)
	if actor.Reason == "" {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "reason_required", "A reason is required")
		return
	}
	planID := chi.URLParam(request, "planID")
	moved, err := handlers.fulfillment.ApplyPlanSquads(request.Context(), planID, "panel:"+actor.RequestID)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	if err = handlers.operations.RecordPlanSquadsApplied(request.Context(), planID, moved, actor); err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]any{"subscriptions": moved})
}

func (handlers *AdminHandlers) subscriptionPolicy(writer http.ResponseWriter, request *http.Request) {
	policy, err := handlers.operations.SubscriptionPolicy(request.Context())
	handlers.respond(writer, request, policy, err)
}

func (handlers *AdminHandlers) saveSubscriptionPolicy(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.SubscriptionPolicy
	if !decodeJSON(writer, request, &body) {
		return
	}
	policy, err := handlers.operations.SaveSubscriptionPolicy(request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, policy, err)
}

// squadListing is the panel's squads as the plan editor renders them.
//
// Reported is false, with a Detail, when the list could not be read. The
// editor then falls back to typing an identifier rather than offering an empty
// picker that reads as "this panel has no squads".
type squadListing struct {
	Reported bool              `json:"reported"`
	Detail   string            `json:"detail,omitempty"`
	Internal []remnawave.Squad `json:"internal"`
	External []remnawave.Squad `json:"external"`
}

func (handlers *AdminHandlers) remnawaveSquads(writer http.ResponseWriter, request *http.Request) {
	listing := squadListing{Internal: []remnawave.Squad{}, External: []remnawave.Squad{}}
	if handlers.remnawave == nil {
		listing.Detail = "remnawave_not_configured"
		writeJSON(writer, http.StatusOK, listing)
		return
	}
	internal, err := handlers.remnawave.InternalSquads(request.Context())
	if err != nil {
		handlers.logger.Warn("remnawave squad listing failed", "error", err)
		listing.Detail = "squads_unavailable"
		writeJSON(writer, http.StatusOK, listing)
		return
	}
	listing.Reported, listing.Internal = true, internal
	// External squads arrived in a later panel release than internal ones. A
	// panel without the route still has a perfectly usable internal list.
	external, err := handlers.remnawave.ExternalSquads(request.Context())
	switch {
	case err == nil:
		listing.External = external
	case errors.Is(err, remnawave.ErrNotFound):
	default:
		handlers.logger.Warn("remnawave external squad listing failed", "error", err)
	}
	writeJSON(writer, http.StatusOK, listing)
}
