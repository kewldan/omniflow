package httpapi

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// mountCapacityChanges registers the mid-period capacity changes of a live
// subscription: switching its traffic package.
func (handlers *AccountHandlers) mountCapacityChanges(router chi.Router) {
	if handlers.checkout == nil {
		return
	}
	router.Get("/subscriptions/{subscriptionID}/traffic-package", handlers.trafficPackages)
	router.Post("/subscriptions/{subscriptionID}/traffic-package", handlers.changeTrafficPackage)
}

// capacityQuotePayload is one priced change as the customer is shown it.
func capacityQuotePayload(quote commercepg.CapacityChangeQuote) map[string]any {
	return map[string]any{
		"currency":            quote.Currency,
		"chargeMinor":         quote.ChargeMinor,
		"creditMinor":         quote.CreditMinor,
		"creditAppliedMinor":  quote.CreditAppliedMinor,
		"creditToWalletMinor": quote.CreditToWalletMinor,
		"walletMinor":         quote.WalletMinor,
		"externalMinor":       quote.ExternalMinor,
		"walletBalanceMinor":  quote.WalletBalanceMinor,
		"endsAt":              quote.EndsAt,
	}
}

func (handlers *AccountHandlers) trafficPackages(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	view, err := handlers.checkout.TrafficPackages(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "subscriptionID"),
		accountLocale(request, principal.Customer.Locale),
	)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	options := make([]map[string]any, 0, len(view.Options))
	for _, option := range view.Options {
		entry := map[string]any{
			"name": option.Name, "trafficBytes": option.TrafficBytes, "direction": option.Direction,
			"quote": capacityQuotePayload(option.Quote),
		}
		if option.AddonVersionID != "" {
			entry["addonVersionId"] = option.AddonVersionID
		}
		options = append(options, entry)
	}
	payload := map[string]any{
		"available": view.Available, "currency": view.Currency,
		"currentTrafficBytes": view.CurrentTrafficBytes, "options": options,
	}
	if view.Reason != "" {
		payload["reason"] = view.Reason
	}
	writeJSON(writer, http.StatusOK, payload)
}

func (handlers *AccountHandlers) changeTrafficPackage(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	if !requireIdempotencyKey(writer, request) {
		return
	}
	var body struct {
		AddonVersionID string `json:"addonVersionId"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	order, err := handlers.checkout.BuyTrafficChange(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "subscriptionID"),
		strings.TrimSpace(body.AddonVersionID), strings.TrimSpace(request.Header.Get("Idempotency-Key")),
		accountLocale(request, principal.Customer.Locale),
	)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusCreated, handlers.orderPayload(request.Context(), order, nil))
}
