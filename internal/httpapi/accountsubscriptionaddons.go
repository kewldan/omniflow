package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
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
	router.Put("/subscriptions/{subscriptionID}/addons/cart", handlers.saveSubscriptionAddonCart)
	router.Delete("/subscriptions/{subscriptionID}/addons/cart", handlers.discardSubscriptionAddonCart)
	router.Get("/subscriptions/{subscriptionID}/traffic-reset", handlers.trafficResetQuote)
	router.Post("/subscriptions/{subscriptionID}/traffic-reset", handlers.buyTrafficReset)
	router.With(handlers.requireRecentAuthentication).
		Delete("/subscriptions/{subscriptionID}", handlers.closeSubscription)
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
	writeJSON(writer, http.StatusOK, subscriptionAddonsPayload(view))
}

func (handlers *AccountHandlers) saveSubscriptionAddonCart(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	var body struct {
		AddonVersionID string `json:"addonVersionId"`
		Quantity       int    `json:"quantity"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	view, err := handlers.checkout.SaveSubscriptionAddonCart(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "subscriptionID"),
		body.AddonVersionID, body.Quantity, accountLocale(request, principal.Customer.Locale),
	)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, subscriptionAddonsPayload(view))
}

func (handlers *AccountHandlers) discardSubscriptionAddonCart(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	view, err := handlers.checkout.DiscardSubscriptionAddonCart(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "subscriptionID"),
		accountLocale(request, principal.Customer.Locale),
	)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, subscriptionAddonsPayload(view))
}

func subscriptionAddonsPayload(view accountcheckout.SubscriptionAddons) map[string]any {
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
	if cart := view.Cart; cart != nil {
		saved := map[string]any{
			"addonVersionId": cart.AddonVersionID, "quantity": cart.Quantity,
			"saved": map[string]any{"amountMinor": cart.SavedMinor, "currency": view.Currency},
			"total": map[string]any{"amountMinor": cart.TotalMinor, "currency": view.Currency},
			"stale": cart.Stale, "autoPurchase": cart.AutoPurchase, "expiresAt": cart.ExpiresAt,
			"missingMinor": max(cart.TotalMinor-view.WalletMinor, 0),
		}
		if cart.LastFailure != "" {
			saved["lastFailure"] = cart.LastFailure
		}
		payload["cart"] = saved
	}
	return payload
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

// closeSubscription closes one of the customer's additional subscriptions.
// Giving up the time a subscription has left cannot be undone, so it needs a
// recent sign-in, as rotating a link does.
func (handlers *AccountHandlers) closeSubscription(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	err := handlers.checkout.CloseSubscription(request.Context(), principal.Customer.ID, chi.URLParam(request, "subscriptionID"))
	switch {
	case errors.Is(err, commercepg.ErrPrimarySubscription):
		writeProblem(writer, request, http.StatusConflict, "subscription_primary", "The first subscription cannot be closed")
		return
	case errors.Is(err, commercepg.ErrSubscriptionBusy):
		writeProblem(writer, request, http.StatusConflict, "subscription_busy", "An unpaid order still targets this subscription")
		return
	}
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AccountHandlers) trafficResetQuote(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	quote, err := handlers.checkout.TrafficResetQuote(request.Context(), principal.Customer.ID, chi.URLParam(request, "subscriptionID"))
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"available": quote.Available,
		"price":     map[string]any{"amountMinor": quote.AmountMinor, "currency": quote.Currency},
	})
}

func (handlers *AccountHandlers) buyTrafficReset(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	if !requireIdempotencyKey(writer, request) {
		return
	}
	order, err := handlers.checkout.BuyTrafficReset(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "subscriptionID"),
		strings.TrimSpace(request.Header.Get("Idempotency-Key")), accountLocale(request, principal.Customer.Locale),
	)
	if errors.Is(err, commerce.ErrTrafficResetUnavailable) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "traffic_reset_unavailable", "This subscription cannot buy a traffic reset")
		return
	}
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusCreated, handlers.orderPayload(request.Context(), order, nil))
}
