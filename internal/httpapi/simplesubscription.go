package httpapi

import (
	"errors"
	"net/http"

	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// simpleOffer is the one plan the store shows when the operator turned the
// simple subscription on; `enabled` is false otherwise and the store shows
// the catalogue.
func (handlers *AccountHandlers) simpleOffer(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	offer, err := handlers.checkout.SimpleOffer(
		request.Context(), principal.Customer.ID, accountLocale(request, principal.Customer.Locale),
	)
	if errors.Is(err, accountcheckout.ErrSimpleUnavailable) {
		writeJSON(writer, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"enabled":       true,
		"planVersionId": offer.PlanVersionID,
		"plan":          planPayload(offer.Plan),
		"periodDays":    offer.PeriodDays,
		"trafficGb":     offer.TrafficGB,
		"extraDevices":  offer.DeviceQuantity * offer.DeviceSlots,
		"total":         map[string]any{"amountMinor": offer.TotalMinor, "currency": offer.Currency},
	})
}

// openSimpleCheckout is the Buy button: it opens the checkout with the simple
// subscription's period, traffic, and devices already chosen.
func (handlers *AccountHandlers) openSimpleCheckout(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	view, err := handlers.checkout.OpenSimple(
		request.Context(), principal.Customer.ID, accountLocale(request, principal.Customer.Locale),
	)
	if errors.Is(err, accountcheckout.ErrSimpleUnavailable) {
		writeProblem(writer, request, http.StatusConflict, "simple_subscription_unavailable", "The simple subscription is not on")
		return
	}
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusCreated, checkoutPayload(view))
}

func (handlers *AdminHandlers) simpleSubscription(writer http.ResponseWriter, request *http.Request) {
	setting, err := handlers.operations.SimpleSubscription(request.Context())
	handlers.respond(writer, request, setting, err)
}

func (handlers *AdminHandlers) saveSimpleSubscription(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.SimpleSubscription
	if !decodeJSON(writer, request, &body) {
		return
	}
	setting, err := handlers.operations.SaveSimpleSubscription(request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, setting, err)
}
