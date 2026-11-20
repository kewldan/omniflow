package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountReminders registers the reminder constructor: repeating reminders by
// segment, delivered by the bot and shown as cards in the customer panel.
func (handlers *AdminHandlers) mountReminders(secure chi.Router) {
	if handlers.operations == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionMarketingRead)).Group(func(read chi.Router) {
		read.Get("/marketing/reminders", handlers.listReminders)
		read.Get("/marketing/reminders/{reminderID}/audience", handlers.reminderAudience)
	})
	secure.With(handlers.requirePermission(rbac.PermissionMarketingWrite)).Group(func(write chi.Router) {
		write.Put("/marketing/reminders", handlers.saveReminder)
		write.Delete("/marketing/reminders/{reminderID}", handlers.deleteReminder)
	})
}

func (handlers *AdminHandlers) listReminders(writer http.ResponseWriter, request *http.Request) {
	reminders, err := handlers.operations.Reminders(request.Context())
	handlers.respond(writer, request, map[string]any{"items": reminders}, err)
}

func (handlers *AdminHandlers) saveReminder(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.Reminder
	if !decodeJSON(writer, request, &body) {
		return
	}
	saved, err := handlers.operations.SaveReminder(request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, saved, err)
}

func (handlers *AdminHandlers) deleteReminder(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.DeleteReminder(request.Context(), chi.URLParam(request, "reminderID"), actorFrom(request))
	handlers.respond(writer, request, map[string]any{"removed": true}, err)
}

func (handlers *AdminHandlers) reminderAudience(writer http.ResponseWriter, request *http.Request) {
	count, err := handlers.operations.ReminderAudience(request.Context(), chi.URLParam(request, "reminderID"))
	handlers.respond(writer, request, map[string]any{"customers": count}, err)
}
