package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountSupportControls mounts the desk's bulk close and message deletion.
// Blocking a customer from support is a customer restriction ("support") on
// the customer card.
func (handlers *AdminHandlers) mountSupportControls(secure chi.Router) {
	if handlers.operations == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionSupportWrite)).Group(func(write chi.Router) {
		write.Post("/support/tickets/close-all", handlers.closeAllTickets)
		write.Delete("/support/tickets/{ticketID}/messages/{messageID}", handlers.deleteSupportMessage)
	})
}

func (handlers *AdminHandlers) closeAllTickets(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		QueueID string `json:"queueId"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	closed, err := handlers.operations.CloseAllSupportTickets(request.Context(), body.QueueID, actorFrom(request))
	handlers.respond(writer, request, map[string]any{"closed": closed}, err)
}

func (handlers *AdminHandlers) deleteSupportMessage(writer http.ResponseWriter, request *http.Request) {
	messageID, err := strconv.ParseInt(chi.URLParam(request, "messageID"), 10, 64)
	if err != nil {
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That message was not found")
		return
	}
	err = handlers.operations.DeleteSupportMessage(request.Context(), chi.URLParam(request, "ticketID"), messageID, actorFrom(request))
	handlers.respond(writer, request, map[string]any{"deleted": true}, err)
}
