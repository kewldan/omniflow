package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountNotificationHistory registers the operator's view of what a customer was
// actually sent, and the test send.
//
// Reading is `customers.read`: a delivery history is part of a customer record,
// and the person answering "did you send it" is the person already looking at
// the account. Sending is `support.write` rather than `customers.write` —
// nothing about the customer changes, a message goes out, and the operators who
// need this are the ones handling the conversation it came from.
func (handlers *AdminHandlers) mountNotificationHistory(secure chi.Router) {
	if handlers.operations == nil {
		return
	}

	secure.With(handlers.requirePermission(rbac.PermissionCustomersRead)).
		Get("/customers/{customerID}/notifications", handlers.customerNotifications)
	secure.With(handlers.requirePermission(rbac.PermissionCustomersRead)).
		Get("/customers/{customerID}/notifications/summary", handlers.customerNotificationSummary)

	secure.With(handlers.requirePermission(rbac.PermissionSupportWrite)).
		Post("/customers/{customerID}/notifications/test", handlers.sendTestNotification)
}

// customerNotifications reads one page of history.
func (handlers *AdminHandlers) customerNotifications(
	writer http.ResponseWriter, request *http.Request,
) {
	page, err := handlers.operations.Deliveries(
		request.Context(), chi.URLParam(request, "customerID"),
		query(request, "kind"), query(request, "status"),
		queryInt(request, "offset"), queryInt(request, "limit"),
	)
	handlers.respond(writer, request, page, err)
}

// customerNotificationSummary is the shape of the whole history, which is what
// somebody wants before reading any single row of it.
func (handlers *AdminHandlers) customerNotificationSummary(
	writer http.ResponseWriter, request *http.Request,
) {
	summaries, err := handlers.operations.DeliverySummaries(
		request.Context(), chi.URLParam(request, "customerID"))
	handlers.respond(writer, request, map[string]any{"summaries": summaries}, err)
}

// sendTestNotification queues one message through the ordinary outbox.
//
// It answers 202 rather than 200 deliberately. The panel process holds no
// Telegram connection; the notifier does, and it collects on its own schedule.
// Reporting a send that has not happened yet as though it had is the failure
// this route exists to make visible, so it does not commit one itself — the
// history below the button is where the outcome appears.
func (handlers *AdminHandlers) sendTestNotification(
	writer http.ResponseWriter, request *http.Request,
) {
	queued, err := handlers.operations.SendTestNotification(
		request.Context(), chi.URLParam(request, "customerID"), actorFrom(request))
	if err != nil {
		handlers.respond(writer, request, nil, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, queued)
}
