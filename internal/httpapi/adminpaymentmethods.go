package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/payments"
)

// listPaymentMethodSettings reports every customer-facing payment button this
// deployment can show — one per method of each configured adapter — with the
// operator's name, description, bounds, quick amounts, and audience.
func (handlers *AdminHandlers) listPaymentMethodSettings(writer http.ResponseWriter, request *http.Request) {
	adapters := make(map[string][]string, len(handlers.providers))
	for name, provider := range handlers.providers {
		adapters[name] = payments.ProviderMethods(provider)
	}
	items, err := handlers.operations.ListPaymentMethodSettings(request.Context(), adapters)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

// savePaymentGuidance stores the manual-payment instructions and whether the
// wallet offers to top up through support.
func (handlers *AdminHandlers) savePaymentGuidance(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.PaymentGuidance
	if !decodeJSON(writer, request, &body) {
		return
	}
	saved, err := handlers.operations.SavePaymentGuidance(request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, saved, err)
}

// savePaymentMethodSettings stores one button's configuration. The customer
// lists read it within half a minute; nothing here changes what an adapter is
// able to do, only how and to whom it is offered.
func (handlers *AdminHandlers) savePaymentMethodSettings(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.PaymentMethodSettings
	if !decodeJSON(writer, request, &body) {
		return
	}
	body.Provider = chi.URLParam(request, "provider")
	saved, err := handlers.operations.SavePaymentMethodSettings(request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, saved, err)
}
