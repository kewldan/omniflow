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
	secure.With(handlers.requirePermission(rbac.PermissionSettingsRead)).
		Get("/settings/subscription-policy", handlers.subscriptionPolicy)
	secure.With(handlers.requirePermission(rbac.PermissionSettingsWrite)).
		Put("/settings/subscription-policy", handlers.saveSubscriptionPolicy)
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
