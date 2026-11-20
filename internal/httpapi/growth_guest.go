package httpapi

import (
	"errors"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/guestpurchase"
)

// mountGuest registers guest purchases on the pre-sign-in router. Starting a
// purchase opens an order, so it is same-origin only and limited per client
// address; the status read needs the token the start returned.
func (handlers *AccountHandlers) mountGuest(account chi.Router) {
	if handlers.growth.Guest == nil {
		return
	}
	account.Get("/guest/options", handlers.guestOptions)
	account.With(requireSameOriginJSON).Post("/guest/purchases", handlers.startGuestPurchase)
	account.Get("/guest/purchases/{purchaseID}", handlers.guestPurchaseStatus)
}

func (handlers *AccountHandlers) guestOptions(writer http.ResponseWriter, request *http.Request) {
	currency := trimmedQuery(request, "currency")
	if currency == "" {
		currency = handlers.growth.GuestCurrency
	}
	providers := []string{}
	for _, option := range handlers.growth.GuestPayments.Options() {
		if !option.Enabled || option.Provider == "telegram_stars" || option.Provider == "manual" {
			continue
		}
		if len(option.Currencies) > 0 && !slices.Contains(option.Currencies, currency) {
			continue
		}
		providers = append(providers, option.Provider)
	}
	sort.Strings(providers)
	writeJSON(writer, http.StatusOK, map[string]any{"currency": currency, "providers": providers})
}

func (handlers *AccountHandlers) startGuestPurchase(writer http.ResponseWriter, request *http.Request) {
	key := idempotencyKey(request)
	if key == "" {
		writeProblem(writer, request, http.StatusBadRequest, "idempotency_key_required", "A purchase requires an Idempotency-Key header")
		return
	}
	subject := ""
	if address := handlers.requestContext(request).IP; address != nil {
		subject = address.String()
	}
	if !handlers.allowPrivacyRate(request, "guest_purchase", subject, 5, time.Hour) {
		writeProblem(writer, request, http.StatusTooManyRequests, "rate_limited", "Too many purchases were started; try again later")
		return
	}
	var body guestpurchase.Input
	if !decodeJSON(writer, request, &body) {
		return
	}
	started, err := handlers.growth.Guest.Start(request.Context(), key, body)
	switch {
	case errors.Is(err, guestpurchase.ErrInvalid):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_request", "Check the plan and the e-mail addresses")
	case errors.Is(err, guestpurchase.ErrProvider):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "provider_unavailable", "That payment method is not available")
	case errors.Is(err, commercepg.ErrMaintenance):
		writeProblem(writer, request, http.StatusServiceUnavailable, "maintenance", "Purchases are paused for maintenance")
	case err != nil && started.ID == "":
		handlers.logger.Error("guest purchase failed", "error", err)
		writeProblem(writer, request, http.StatusUnprocessableEntity, "purchase_failed", "The purchase could not be started")
	case err != nil:
		// The order exists but the provider refused the payment; the page can
		// retry with the same key.
		handlers.logger.Warn("guest payment could not be opened", "error", err)
		writeProblem(writer, request, http.StatusBadGateway, "payment_unavailable", "The payment could not be opened; try again")
	default:
		writeJSON(writer, http.StatusCreated, started)
	}
}

func (handlers *AccountHandlers) guestPurchaseStatus(writer http.ResponseWriter, request *http.Request) {
	status, err := handlers.growth.Guest.Status(request.Context(), chi.URLParam(request, "purchaseID"), trimmedQuery(request, "token"))
	if err != nil {
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That purchase does not exist")
		return
	}
	writeJSON(writer, http.StatusOK, status)
}
