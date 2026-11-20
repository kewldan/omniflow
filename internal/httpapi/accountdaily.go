package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// dailyBilling reports how a subscription on a daily plan is billed, so the
// subscription page can offer the customer's pause.
func (handlers *AccountHandlers) dailyBilling(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	status, err := handlers.checkout.DailyBilling(request.Context(), principal.Customer.ID, chi.URLParam(request, "subscriptionID"))
	if handlers.writeDailyError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, dailyPayload(status))
}

// setDailyBilling pauses or resumes a daily subscription. Pausing stops the
// daily charge and disables access through fulfillment; resuming gives back
// the part of the day the pause held.
func (handlers *AccountHandlers) setDailyBilling(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	if !requireIdempotencyKey(writer, request) {
		return
	}
	var body struct {
		Paused *bool `json:"paused"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	if body.Paused == nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_request", "paused is required")
		return
	}
	status, err := handlers.checkout.SetDailyPause(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "subscriptionID"), *body.Paused,
		strings.TrimSpace(request.Header.Get("Idempotency-Key")),
	)
	if handlers.writeDailyError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, dailyPayload(status))
}

func (handlers *AccountHandlers) writeDailyError(writer http.ResponseWriter, request *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, commercepg.ErrNotDaily):
		writeProblem(writer, request, http.StatusConflict, "not_daily", "This subscription is not billed by the day")
	case errors.Is(err, commercepg.ErrNotPausable):
		writeProblem(writer, request, http.StatusConflict, "not_pausable", "This subscription is not in a state that can be paused or resumed")
	case errors.Is(err, commercepg.ErrSubscriptionUnknown):
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That subscription was not found")
	default:
		return handlers.writeCheckoutError(writer, request, err)
	}
	return true
}

func dailyPayload(status commercepg.DailyStatus) map[string]any {
	payload := map[string]any{"daily": status.Daily}
	if status.Daily {
		payload["paused"] = status.Paused
		payload["suspended"] = status.Suspended
		payload["endsAt"] = status.EndsAt.Format(time.RFC3339)
		payload["price"] = map[string]any{"amountMinor": status.PriceMinor, "currency": status.Currency}
	}
	return payload
}
