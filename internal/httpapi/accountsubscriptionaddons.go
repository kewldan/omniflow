package httpapi

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// mountSubscriptionAddons registers the mid-period add-on routes: what may be
// added to a live subscription, and buying one. The bot offered these alone;
// the web panel now buys through the same order path.
func (handlers *AccountHandlers) mountSubscriptionAddons(router chi.Router) {
	if handlers.checkout == nil {
		return
	}
	router.Get("/subscriptions/{subscriptionID}/addons", handlers.subscriptionAddons)
	router.Post("/subscriptions/{subscriptionID}/addons", handlers.buySubscriptionAddon)
}

func (handlers *AccountHandlers) subscriptionAddons(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	view, err := handlers.checkout.SubscriptionAddons(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "subscriptionID"),
		accountLocale(request, principal.Customer.Locale),
	)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	addons := make([]map[string]any, 0, len(view.Addons))
	for _, addon := range view.Addons {
		entry := addonPayload(addon.AddonOffer)
		entry["charge"] = map[string]any{"amountMinor": addon.ChargedMinor, "currency": addon.Currency}
		addons = append(addons, entry)
	}
	payload := map[string]any{
		"subscriptionId": view.SubscriptionID, "available": view.Available,
		"currency": view.Currency, "walletMinor": view.WalletMinor, "addons": addons,
	}
	if view.Reason != "" {
		payload["reason"] = view.Reason
	}
	if !view.EndsAt.IsZero() {
		payload["endsAt"] = view.EndsAt
	}
	writeJSON(writer, http.StatusOK, payload)
}

func (handlers *AccountHandlers) buySubscriptionAddon(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	if !requireIdempotencyKey(writer, request) {
		return
	}
	var body struct {
		AddonVersionID string `json:"addonVersionId"`
		Quantity       int    `json:"quantity"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	order, err := handlers.checkout.BuySubscriptionAddon(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "subscriptionID"),
		body.AddonVersionID, body.Quantity, strings.TrimSpace(request.Header.Get("Idempotency-Key")),
		accountLocale(request, principal.Customer.Locale),
	)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusCreated, handlers.orderPayload(request.Context(), order, nil))
}
