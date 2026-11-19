package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountPlatform mounts the platform administration surfaces: the application
// error log, and the routes later features add beside it.
func (handlers *AdminHandlers) mountPlatform(secure chi.Router) {
	if handlers.operations == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionSystemRead)).
		Get("/system/errors", handlers.listAppErrors)
	secure.With(handlers.requirePermission(rbac.PermissionSystemWrite)).
		Post("/system/errors/{errorID}/resolve", handlers.resolveAppError)
	secure.With(handlers.requirePermission(rbac.PermissionSystemWrite)).
		Post("/system/errors/resolve-all", handlers.resolveAllAppErrors)
}

func (handlers *AdminHandlers) listAppErrors(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	items, err := handlers.operations.AppErrors(request.Context(), query.Get("process"), query.Get("resolved") == "true")
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) resolveAppError(writer http.ResponseWriter, request *http.Request) {
	resolved, err := handlers.operations.ResolveAppError(
		request.Context(), chi.URLParam(request, "errorID"), actorFrom(request))
	handlers.respond(writer, request, resolved, err)
}

func (handlers *AdminHandlers) resolveAllAppErrors(writer http.ResponseWriter, request *http.Request) {
	count, err := handlers.operations.ResolveAllAppErrors(request.Context(), actorFrom(request))
	handlers.respond(writer, request, map[string]any{"resolved": count}, err)
}
