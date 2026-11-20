package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/accountcheckout"
)

// mountOffers registers the customer's personal offers: the list, taking one
// (which starts its timer and pays its bonus), and dismissing one, which is
// how a customer resets an active discount they do not want.
func (handlers *AccountHandlers) mountOffers(router chi.Router) {
	if handlers.checkout == nil {
		return
	}
	router.Get("/offers", handlers.listOffers)
	router.Post("/offers/{offerID}/take", handlers.takeOffer)
	router.Post("/offers/{offerID}/dismiss", handlers.dismissOffer)
}

func (handlers *AccountHandlers) listOffers(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	offers, err := handlers.checkout.Offers(
		request.Context(), principal.Customer.ID, accountLocale(request, principal.Customer.Locale),
	)
	if err != nil {
		handlers.logger.Error("offers lookup failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "offers_unavailable", "Offers could not be loaded")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": offers})
}

func (handlers *AccountHandlers) takeOffer(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	taken, err := handlers.checkout.TakeOffer(request.Context(), principal.Customer.ID, chi.URLParam(request, "offerID"))
	if handlers.offerError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, taken)
}

func (handlers *AccountHandlers) dismissOffer(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	err := handlers.checkout.DismissOffer(request.Context(), principal.Customer.ID, chi.URLParam(request, "offerID"))
	if handlers.offerError(writer, request, err) {
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AccountHandlers) offerError(writer http.ResponseWriter, request *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, accountcheckout.ErrOfferNotFound):
		writeProblem(writer, request, http.StatusNotFound, "offer_not_found", "That offer is no longer available")
	default:
		handlers.logger.Error("offer action failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "offer_failed", "The offer could not be updated")
	}
	return true
}
