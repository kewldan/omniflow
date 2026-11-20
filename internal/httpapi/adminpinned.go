package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountPinned registers the pinned message for every customer.
func (handlers *AdminHandlers) mountPinned(secure chi.Router) {
	if handlers.operations == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionMarketingRead)).
		Get("/marketing/pinned", handlers.pinnedMessages)
	secure.With(handlers.requirePermission(rbac.PermissionMarketingWrite)).Group(func(write chi.Router) {
		write.Post("/marketing/pinned", handlers.publishPinned)
		write.Post("/marketing/pinned/{pinnedID}/retire", handlers.retirePinned)
	})
}

func (handlers *AdminHandlers) pinnedMessages(writer http.ResponseWriter, request *http.Request) {
	messages, err := handlers.operations.PinnedMessages(request.Context())
	handlers.respond(writer, request, map[string]any{"items": messages}, err)
}

func (handlers *AdminHandlers) publishPinned(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.PinnedMessage
	if !decodeJSON(writer, request, &body) {
		return
	}
	message, err := handlers.operations.PublishPinned(request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, message, err)
}

func (handlers *AdminHandlers) retirePinned(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.RetirePinned(request.Context(), chi.URLParam(request, "pinnedID"), actorFrom(request))
	handlers.respond(writer, request, map[string]any{"retired": err == nil}, err)
}
