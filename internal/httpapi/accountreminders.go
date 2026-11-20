package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// accountReminders lists the reminder cards the customer's segment holds
// now, in their language.
func (handlers *AccountHandlers) accountReminders(writer http.ResponseWriter, request *http.Request) {
	if !handlers.requireAccount(writer, request) {
		return
	}
	principal, _ := CustomerFrom(request.Context())
	var userID pgtype.UUID
	if userID.Scan(principal.Customer.ID) != nil {
		writeProblem(writer, request, http.StatusNotFound, "not_found", "Not found")
		return
	}
	rows, err := dbgen.New(handlers.account.Pool()).ListCabinetReminders(request.Context(), userID)
	if err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "reminders_unavailable", "Unavailable")
		return
	}
	russian := principal.Customer.Locale == "ru"
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		title, body := row.TitleEn, row.BodyEn
		if russian {
			title, body = row.TitleRu, row.BodyRu
		}
		item := map[string]any{"id": uuidText(row.ID), "title": title, "body": body}
		if row.ButtonRoute.Valid {
			item["route"] = row.ButtonRoute.String
		}
		items = append(items, item)
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

// dismissReminder hides a reminder card for good, and stops its messages.
func (handlers *AccountHandlers) dismissReminder(writer http.ResponseWriter, request *http.Request) {
	if !handlers.requireAccount(writer, request) {
		return
	}
	principal, _ := CustomerFrom(request.Context())
	var userID, reminderID pgtype.UUID
	if userID.Scan(principal.Customer.ID) != nil || reminderID.Scan(chi.URLParam(request, "reminderID")) != nil {
		writeProblem(writer, request, http.StatusNotFound, "not_found", "Not found")
		return
	}
	if err := dbgen.New(handlers.account.Pool()).DismissReminder(request.Context(), dbgen.DismissReminderParams{
		UserID: userID, ReminderID: reminderID,
	}); err != nil {
		writeProblem(writer, request, http.StatusInternalServerError, "dismiss_failed", "It could not be dismissed")
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func uuidText(id pgtype.UUID) string {
	value, err := id.Value()
	if err != nil {
		return ""
	}
	text, _ := value.(string)
	return text
}
