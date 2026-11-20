package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/nalogo"
	"github.com/omniflow/omniflow/internal/rbac"
	"github.com/omniflow/omniflow/internal/selfemployed"
)

// mountSelfEmployed registers the «Мой налог» receipts. Reading the queue is
// finance.read; the credentials, a connection check, and a retry are
// finance.write, because the password files income on the operator's behalf.
func (handlers *AdminHandlers) mountSelfEmployed(secure chi.Router) {
	if handlers.growth.SelfEmployed == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionFinanceRead)).Group(func(read chi.Router) {
		read.Get("/finance/self-employed", handlers.selfEmployedReceipts)
		read.Get("/finance/self-employed/settings", handlers.selfEmployedSettings)
	})
	secure.With(handlers.requirePermission(rbac.PermissionFinanceWrite)).Group(func(write chi.Router) {
		write.Put("/finance/self-employed/settings", handlers.saveSelfEmployedSettings)
		write.Post("/finance/self-employed/check", handlers.checkSelfEmployed)
		write.Post("/finance/self-employed/{receiptID}/retry", handlers.retrySelfEmployedReceipt)
	})
}

func (handlers *AdminHandlers) selfEmployedReceipts(writer http.ResponseWriter, request *http.Request) {
	items, err := handlers.growth.SelfEmployed.List(request.Context(), query(request, "status"), int(queryInt(request, "limit")))
	handlers.respond(writer, request, map[string]any{"items": items}, err)
}

func (handlers *AdminHandlers) selfEmployedSettings(writer http.ResponseWriter, request *http.Request) {
	settings, err := handlers.growth.SelfEmployed.Settings(request.Context())
	handlers.respond(writer, request, settings, err)
}

func (handlers *AdminHandlers) saveSelfEmployedSettings(writer http.ResponseWriter, request *http.Request) {
	var body selfemployed.SettingsInput
	if !decodeJSON(writer, request, &body) {
		return
	}
	settings, err := handlers.growth.SelfEmployed.SaveSettings(request.Context(), body, actorFrom(request))
	handlers.selfEmployedResponse(writer, request, settings, err)
}

func (handlers *AdminHandlers) checkSelfEmployed(writer http.ResponseWriter, request *http.Request) {
	err := handlers.growth.SelfEmployed.CheckConnection(request.Context(), actorFrom(request))
	switch {
	case errors.Is(err, nalogo.ErrUnauthorized):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "credentials_refused", "Moi Nalog refused the taxpayer number or password")
	case errors.Is(err, nalogo.ErrUncertain), errors.Is(err, nalogo.ErrRejected):
		writeProblem(writer, request, http.StatusBadGateway, "service_unavailable", "Moi Nalog did not answer; try again later")
	default:
		handlers.selfEmployedResponse(writer, request, map[string]bool{"ok": true}, err)
	}
}

func (handlers *AdminHandlers) retrySelfEmployedReceipt(writer http.ResponseWriter, request *http.Request) {
	err := handlers.growth.SelfEmployed.Retry(request.Context(), chi.URLParam(request, "receiptID"), actorFrom(request))
	handlers.selfEmployedResponse(writer, request, map[string]bool{"ok": true}, err)
}

func (handlers *AdminHandlers) selfEmployedResponse(writer http.ResponseWriter, request *http.Request, value any, err error) {
	switch {
	case errors.Is(err, selfemployed.ErrInvalid):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_request",
			"Enter a 12-digit taxpayer number and a password before switching receipts on")
	case errors.Is(err, selfemployed.ErrNotFound):
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That receipt does not exist")
	case errors.Is(err, selfemployed.ErrState):
		writeProblem(writer, request, http.StatusConflict, "state_conflict", "Only a failed receipt can be retried")
	default:
		handlers.respond(writer, request, value, err)
	}
}

// mountAccountSelfEmployed lets a customer fetch the receipt for their own order.
func (handlers *AccountHandlers) mountAccountSelfEmployed(secure chi.Router) {
	if handlers.growth.SelfEmployed == nil {
		return
	}
	secure.Get("/orders/{orderID}/tax-receipt", handlers.orderTaxReceipt)
}

func (handlers *AccountHandlers) orderTaxReceipt(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	url, err := handlers.growth.SelfEmployed.CustomerReceiptURL(request.Context(), principal.Customer.ID, chi.URLParam(request, "orderID"))
	if err != nil {
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That order has no receipt")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"receiptUrl": url})
}
