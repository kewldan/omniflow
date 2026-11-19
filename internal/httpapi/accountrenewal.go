package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/accountcheckout"
)

// mountRenewal exposes automatic renewal and saved payment methods to the
// customer web panel. The bot has had these screens since auto-renew existed;
// the routes below read and write the same rows through internal/recurringpg.
func (handlers *AccountHandlers) mountRenewal(router chi.Router) {
	if handlers.checkout == nil {
		return
	}
	router.Get("/renewal", handlers.readRenewal)
	router.Put("/renewal/{subscriptionID}", handlers.updateRenewal)
	router.Post("/payment-methods/{methodID}/default", handlers.defaultPaymentMethod)
	router.Delete("/payment-methods/{methodID}", handlers.removePaymentMethod)
}

func (handlers *AccountHandlers) readRenewal(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	handlers.writeRenewal(writer, request, principal.Customer.ID, accountLocale(request, principal.Customer.Locale))
}

func (handlers *AccountHandlers) writeRenewal(writer http.ResponseWriter, request *http.Request, customerID, locale string) {
	overview, err := handlers.checkout.Renewal(request.Context(), customerID, locale)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, renewalPayload(overview))
}

func (handlers *AccountHandlers) updateRenewal(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	var body struct {
		Enabled       *bool  `json:"enabled"`
		Funding       string `json:"funding"`
		LeadTimeHours int    `json:"leadTimeHours"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.checkout.SetRenewal(request.Context(), principal.Customer.ID, chi.URLParam(request, "subscriptionID"), accountcheckout.RenewalInput{
		Enabled: body.Enabled, Funding: body.Funding, LeadTime: time.Duration(body.LeadTimeHours) * time.Hour,
	})
	if handlers.writeRenewalError(writer, request, err) {
		return
	}
	handlers.writeRenewal(writer, request, principal.Customer.ID, accountLocale(request, principal.Customer.Locale))
}

func (handlers *AccountHandlers) defaultPaymentMethod(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	err := handlers.checkout.SetDefaultMethod(request.Context(), principal.Customer.ID, chi.URLParam(request, "methodID"))
	if handlers.writeRenewalError(writer, request, err) {
		return
	}
	handlers.writeRenewal(writer, request, principal.Customer.ID, accountLocale(request, principal.Customer.Locale))
}

func (handlers *AccountHandlers) removePaymentMethod(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	err := handlers.checkout.RemoveMethod(request.Context(), principal.Customer.ID, chi.URLParam(request, "methodID"))
	if handlers.writeRenewalError(writer, request, err) {
		return
	}
	handlers.writeRenewal(writer, request, principal.Customer.ID, accountLocale(request, principal.Customer.Locale))
}

func (handlers *AccountHandlers) writeRenewalError(writer http.ResponseWriter, request *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, accountcheckout.ErrMethodNotFound):
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That payment method was not found")
	case errors.Is(err, accountcheckout.ErrRecurringUnavailable):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "recurring_unavailable", "No payment method can renew this subscription automatically")
	case errors.Is(err, accountcheckout.ErrNoSavedMethod):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "no_saved_method", "Save a card on a payment first")
	default:
		return handlers.writeCheckoutError(writer, request, err)
	}
	return true
}

func renewalPayload(overview accountcheckout.RenewalOverview) map[string]any {
	methods := make([]map[string]any, 0, len(overview.Methods))
	for _, method := range overview.Methods {
		methods = append(methods, map[string]any{
			"id": method.ID, "provider": method.Provider, "label": method.Label,
			"status": method.Status, "isDefault": method.IsDefault, "createdAt": method.CreatedAt.UTC(),
		})
	}
	subscriptions := make([]map[string]any, 0, len(overview.Subscriptions))
	for _, entry := range overview.Subscriptions {
		item := map[string]any{
			"subscriptionId": entry.Target.ID, "label": entry.Target.Label, "planName": entry.Target.PlanName,
			"supported": entry.Supported, "enabled": entry.Settings.Enabled, "funding": entry.Settings.Funding,
			"leadTimeHours": int(entry.Settings.LeadTime / time.Hour), "state": entry.Settings.State,
			"methodLabel": entry.Settings.MethodLabel,
		}
		if !entry.Target.EndsAt.IsZero() {
			item["endsAt"] = entry.Target.EndsAt
		}
		subscriptions = append(subscriptions, item)
	}
	return map[string]any{"available": overview.Available, "methods": methods, "subscriptions": subscriptions}
}
