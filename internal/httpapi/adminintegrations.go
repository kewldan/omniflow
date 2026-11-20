package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountIntegrations mounts named API tokens and outgoing webhooks. Both are
// ways for another system to act on or hear from the installation, so
// reading them is system.read and changing them is system.write.
func (handlers *AdminHandlers) mountIntegrations(secure chi.Router) {
	if handlers.operations == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionSystemRead)).Group(func(read chi.Router) {
		read.Get("/integrations/tokens", handlers.listAPITokens)
		read.Get("/integrations/tokens/{tokenID}/requests", handlers.apiTokenRequests)
		read.Get("/integrations/webhooks", handlers.listWebhooks)
		read.Get("/integrations/webhooks/{endpointID}/deliveries", handlers.webhookDeliveries)
	})
	secure.With(handlers.requirePermission(rbac.PermissionSystemWrite)).Group(func(write chi.Router) {
		write.Post("/integrations/tokens", handlers.createAPIToken)
		write.Post("/integrations/tokens/{tokenID}/revoke", handlers.revokeAPIToken)
		write.Post("/integrations/webhooks", handlers.createWebhook)
		write.Put("/integrations/webhooks/{endpointID}/enabled", handlers.setWebhookEnabled)
		write.Delete("/integrations/webhooks/{endpointID}", handlers.deleteWebhook)
	})
}

func (handlers *AdminHandlers) listAPITokens(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.APITokens(request.Context())
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) apiTokenRequests(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.APITokenRequests(request.Context(), chi.URLParam(request, "tokenID"))
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) createAPIToken(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	token, err := handlers.operations.CreateAPIToken(request.Context(), body.Name, actorFrom(request))
	handlers.respond(writer, request, token, err)
}

func (handlers *AdminHandlers) revokeAPIToken(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.RevokeAPIToken(request.Context(), chi.URLParam(request, "tokenID"), actorFrom(request))
	handlers.respond(writer, request, map[string]any{"revoked": true}, err)
}

func (handlers *AdminHandlers) listWebhooks(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.Webhooks(request.Context())
	handlers.respond(writer, request, map[string]any{"items": items, "topics": panelpg.WebhookTopics}, err)
}

func (handlers *AdminHandlers) webhookDeliveries(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.operations.WebhookDeliveries(request.Context(), chi.URLParam(request, "endpointID"))
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) createWebhook(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.WebhookInput
	if !decodeJSON(writer, request, &body) {
		return
	}
	endpoint, err := handlers.operations.CreateWebhook(request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, endpoint, err)
}

func (handlers *AdminHandlers) setWebhookEnabled(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.operations.SetWebhookEnabled(request.Context(), chi.URLParam(request, "endpointID"), body.Enabled, actorFrom(request))
	handlers.respond(writer, request, map[string]any{"enabled": body.Enabled}, err)
}

func (handlers *AdminHandlers) deleteWebhook(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.DeleteWebhook(request.Context(), chi.URLParam(request, "endpointID"), actorFrom(request))
	handlers.respond(writer, request, map[string]any{"deleted": true}, err)
}
