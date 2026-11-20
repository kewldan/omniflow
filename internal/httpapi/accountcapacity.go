package httpapi

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/accountpg"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// mountCapacityChanges registers the mid-period capacity changes of a live
// subscription: switching its traffic package and giving back device slots.
func (handlers *AccountHandlers) mountCapacityChanges(router chi.Router) {
	if handlers.checkout == nil {
		return
	}
	router.Get("/subscriptions/{subscriptionID}/traffic-package", handlers.trafficPackages)
	router.Post("/subscriptions/{subscriptionID}/traffic-package", handlers.changeTrafficPackage)
	router.Get("/subscriptions/{subscriptionID}/device-limit", handlers.deviceReduction)
	router.Post("/subscriptions/{subscriptionID}/device-limit", handlers.reduceDevices)
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

// connectedDevices reads the subscription's devices from Remnawave. A
// subscription never provisioned has none; a panel that does not answer
// cannot vouch for the count, so the change is refused rather than guessed.
func (handlers *AccountHandlers) connectedDevices(writer http.ResponseWriter, request *http.Request, customerID, subscriptionID string) ([]accountpg.Device, bool) {
	if handlers.account == nil {
		writeProblem(writer, request, http.StatusServiceUnavailable, "remnawave_unavailable", "Connected devices cannot be read right now")
		return nil, false
	}
	devices, err := handlers.account.Devices(request.Context(), customerID, subscriptionID)
	switch {
	case err == nil:
		return devices, true
	case errors.Is(err, accountpg.ErrNotProvisioned):
		return nil, true
	case errors.Is(err, accountpg.ErrRemnawaveUnavailable):
		writeProblem(writer, request, http.StatusBadGateway, "remnawave_unavailable", "Connected devices cannot be read right now")
		return nil, false
	default:
		handlers.writeCheckoutError(writer, request, err)
		return nil, false
	}
}

func (handlers *AccountHandlers) deviceReduction(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	subscriptionID := chi.URLParam(request, "subscriptionID")
	devices, ok := handlers.connectedDevices(writer, request, principal.Customer.ID, subscriptionID)
	if !ok {
		return
	}
	view, err := handlers.checkout.DeviceReduction(request.Context(), principal.Customer.ID, subscriptionID, len(devices))
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	// The devices seen least recently are the ones a lower limit would leave
	// over first; Remnawave does not rank them, so this is a suggestion and
	// the customer chooses which to disconnect.
	sort.SliceStable(devices, func(left, right int) bool { return devices[left].LastSeen.After(devices[right].LastSeen) })
	listed := make([]map[string]any, 0, len(devices))
	for _, device := range devices {
		listed = append(listed, map[string]any{
			"handle": device.Handle, "name": device.Name, "platform": device.Platform, "alias": device.Alias,
			"lastSeen": device.LastSeen.Format(time.RFC3339),
		})
	}
	options := make([]map[string]any, 0, len(view.Options))
	for _, option := range view.Options {
		options = append(options, map[string]any{
			"deviceLimit": option.DeviceLimit, "excessDevices": option.Quote.ExcessDevices,
			"quote": capacityQuotePayload(option.Quote),
		})
	}
	payload := map[string]any{
		"available": view.Available, "currency": view.Currency,
		"currentLimit": view.CurrentLimit, "minimumLimit": view.MinimumLimit,
		"connected": view.Connected, "devices": listed, "options": options,
	}
	if view.Reason != "" {
		payload["reason"] = view.Reason
	}
	writeJSON(writer, http.StatusOK, payload)
}

func (handlers *AccountHandlers) reduceDevices(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	if !requireIdempotencyKey(writer, request) {
		return
	}
	var body struct {
		DeviceLimit int `json:"deviceLimit"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	subscriptionID := chi.URLParam(request, "subscriptionID")
	devices, ok := handlers.connectedDevices(writer, request, principal.Customer.ID, subscriptionID)
	if !ok {
		return
	}
	order, err := handlers.checkout.ReduceDevices(
		request.Context(), principal.Customer.ID, subscriptionID, body.DeviceLimit, len(devices),
		strings.TrimSpace(request.Header.Get("Idempotency-Key")), accountLocale(request, principal.Customer.Locale),
	)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusCreated, handlers.orderPayload(request.Context(), order, nil))
}
