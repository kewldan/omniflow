package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountConnectCatalogue registers the connection guidance surfaces.
//
// It sits under settings rather than under the catalogue because it configures
// how the product is used rather than what is sold: nothing here has a price, a
// version, or an effect on an order.
func (handlers *AdminHandlers) mountConnectCatalogue(secure chi.Router) {
	if handlers.operations == nil {
		return
	}

	secure.With(handlers.requirePermission(rbac.PermissionSettingsRead)).
		Get("/settings/connect", handlers.connectCatalogue)

	secure.With(handlers.requirePermission(rbac.PermissionSettingsWrite)).Group(func(write chi.Router) {
		write.Put("/settings/connect/platforms", handlers.saveConnectPlatform)
		write.Delete("/settings/connect/platforms/{slug}", handlers.deleteConnectPlatform)
		write.Put("/settings/connect/clients", handlers.saveConnectClient)
		write.Delete("/settings/connect/clients/{clientID}", handlers.deleteConnectClient)
	})
}

func (handlers *AdminHandlers) connectCatalogue(writer http.ResponseWriter, request *http.Request) {
	catalogue, err := handlers.operations.ConnectCatalogue(request.Context())
	handlers.respond(writer, request, catalogue, err)
}

func (handlers *AdminHandlers) saveConnectPlatform(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.ConnectPlatform
	if !decodeJSON(writer, request, &body) {
		return
	}
	platform, err := handlers.operations.SaveConnectPlatform(
		request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, platform, err)
}

func (handlers *AdminHandlers) deleteConnectPlatform(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.DeleteConnectPlatform(
		request.Context(), chi.URLParam(request, "slug"), actorFrom(request))
	handlers.respond(writer, request, map[string]any{"removed": true}, err)
}

func (handlers *AdminHandlers) saveConnectClient(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.ConnectClient
	if !decodeJSON(writer, request, &body) {
		return
	}
	client, err := handlers.operations.SaveConnectClient(
		request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, client, err)
}

func (handlers *AdminHandlers) deleteConnectClient(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.DeleteConnectClient(
		request.Context(), chi.URLParam(request, "clientID"), actorFrom(request))
	handlers.respond(writer, request, map[string]any{"removed": true}, err)
}
