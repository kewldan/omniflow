package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/termsgate"
)

// WithTermsGate attaches the gate that holds the panel until the terms are
// accepted, when the operator requires it.
func (handlers *AccountHandlers) WithTermsGate(gate *termsgate.Gate) *AccountHandlers {
	handlers.terms = gate
	return handlers
}

func (handlers *AccountHandlers) mountTerms(secure chi.Router) {
	if handlers.terms == nil {
		return
	}
	secure.Get("/terms", handlers.termsStatus)
	secure.Post("/terms/accept", handlers.acceptTerms)
}

// termsStatus tells the panel whether to show the terms gate, and which
// documents the customer agrees to by accepting.
func (handlers *AccountHandlers) termsStatus(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	policy := handlers.terms.Policy(request.Context())
	accepted, err := handlers.terms.Accepted(request.Context(), principal.Customer.ID, policy.Version)
	if err != nil {
		handlers.logger.Error("terms acceptance lookup failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "terms_unavailable", "The terms could not be read")
		return
	}
	documents := []map[string]string{}
	if policy.Required && !accepted {
		found, err := handlers.terms.Documents(request.Context(), accountLocale(request, principal.Customer.Locale))
		if err != nil {
			handlers.logger.Error("terms documents could not be read", "error", err)
			writeProblem(writer, request, http.StatusInternalServerError, "terms_unavailable", "The terms could not be read")
			return
		}
		for _, document := range found {
			documents = append(documents, map[string]string{"kind": document.Kind, "slug": document.Slug, "title": document.Title})
		}
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"required": policy.Required, "accepted": accepted, "version": policy.Version,
		"termsUrl": policy.TermsURL, "documents": documents,
	})
}

// acceptTerms records the customer's acceptance of the current version.
func (handlers *AccountHandlers) acceptTerms(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	var body struct {
		Version string `json:"version"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	policy := handlers.terms.Policy(request.Context())
	if body.Version != policy.Version {
		// The customer accepted what their screen showed. If the operator
		// raised the version meanwhile, they have not accepted this one.
		writeProblem(writer, request, http.StatusConflict, "terms_changed", "The terms changed; read them again")
		return
	}
	if err := handlers.terms.Accept(request.Context(), principal.Customer.ID, policy.Version, "web_panel", middlewareRequestID(request)); err != nil {
		handlers.logger.Error("terms acceptance could not be recorded", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "terms_unavailable", "The acceptance could not be recorded")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"required": policy.Required, "accepted": true, "version": policy.Version, "documents": []map[string]string{}})
}

// termsHold refuses a purchase from a customer who has not accepted the terms
// the operator requires, and reports whether it did.
func (handlers *AccountHandlers) termsHold(writer http.ResponseWriter, request *http.Request) bool {
	if handlers.terms == nil {
		return false
	}
	principal, _ := CustomerFrom(request.Context())
	blocked, _, err := handlers.terms.Blocked(request.Context(), principal.Customer.ID)
	if err != nil || !blocked {
		return false
	}
	writeProblem(writer, request, http.StatusForbidden, "terms_required", "Accept the terms before buying")
	return true
}
