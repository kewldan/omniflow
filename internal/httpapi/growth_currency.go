package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/fxrate"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountCurrency registers the exchange-rate settings: the Stars price of a
// ruble and the USD/RUB source. Reading is finance.read; changing what a
// Star costs is finance.write.
func (handlers *AdminHandlers) mountCurrency(secure chi.Router) {
	if handlers.growth.Rates == nil {
		return
	}
	secure.With(handlers.requirePermission(rbac.PermissionFinanceRead)).Get("/finance/currency", handlers.currency)
	secure.With(handlers.requirePermission(rbac.PermissionFinanceWrite)).Put("/finance/currency", handlers.saveCurrency)
}

func (handlers *AdminHandlers) currency(writer http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	settings, err := handlers.growth.Rates.Settings(ctx)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	response := map[string]any{"settings": settings}
	// The live readings are informative: a source being down is shown, not
	// turned into an error for the whole screen.
	if usd, usdErr := handlers.growth.Rates.USDRUB(ctx); usdErr == nil {
		response["usdRub"] = usd
	}
	if currency, minor, ok, starsErr := handlers.growth.Rates.StarsRate(ctx); starsErr == nil && ok {
		response["starsRate"] = map[string]any{"currency": currency, "minor": minor}
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handlers *AdminHandlers) saveCurrency(writer http.ResponseWriter, request *http.Request) {
	var body fxrate.Settings
	if !decodeJSON(writer, request, &body) {
		return
	}
	settings, err := handlers.growth.Rates.SaveSettings(request.Context(), body, actorFrom(request))
	if errors.Is(err, fxrate.ErrInvalid) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_request",
			"Check the rates; the automatic Stars price works only for rubles, and a manual USD/RUB needs a value")
		return
	}
	handlers.respond(writer, request, settings, err)
}
