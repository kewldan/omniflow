package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/emailpg"
	"github.com/omniflow/omniflow/internal/mailer"
	"github.com/omniflow/omniflow/internal/rbac"
	"github.com/omniflow/omniflow/internal/referralpayout"
)

// GrowthServices are the services that close the gap with Bedolaga: e-mail,
// referral withdrawals, self-employed receipts, exchange rates, guest
// purchases, and engagement. Each is optional; a nil service leaves its
// routes unmounted.
type GrowthServices struct {
	Email *emailpg.Service
	// EmailSending reports whether the worker has a transport, which the api
	// can only know from the shared configuration.
	EmailSending bool
	// Withdrawals is the referral balance withdrawal service.
	Withdrawals *referralpayout.Service
}

// WithGrowth attaches the growth services to the panel.
func (handlers *AdminHandlers) WithGrowth(services GrowthServices) *AdminHandlers {
	handlers.growth = services
	return handlers
}

// WithGrowth attaches the growth services to the customer API.
func (handlers *AccountHandlers) WithGrowth(services GrowthServices) *AccountHandlers {
	handlers.growth = services
	return handlers
}

func (handlers *AdminHandlers) mountGrowth(secure chi.Router) {
	if handlers.growth.Email != nil {
		secure.With(handlers.requirePermission(rbac.PermissionSettingsRead)).
			Get("/settings/email", handlers.emailStatus)
		secure.With(handlers.requirePermission(rbac.PermissionSettingsWrite)).
			Post("/settings/email/test", handlers.emailTest)
	}
	handlers.mountWithdrawals(secure)
}

// mountGrowthPublic registers the routes reachable without a session.
func (handlers *AccountHandlers) mountGrowthPublic(account chi.Router) {
	if handlers.growth.Email != nil {
		// RFC 8058: the mail client posts here on the reader's behalf, with no
		// cookie and from no origin, so the route has neither a session nor a
		// same-origin check. The signed token is the whole authorisation, and
		// all it can do is stop marketing mail.
		account.Post("/email/unsubscribe", handlers.emailUnsubscribe)
		// A person clicking the footer link lands on a confirmation page rather
		// than being unsubscribed by a GET, which link scanners follow.
		account.Get("/email/unsubscribe", handlers.emailUnsubscribePage)
	}
}

func (handlers *AccountHandlers) mountGrowth(secure chi.Router) {
	handlers.mountAccountWithdrawals(secure)
}

func (handlers *AdminHandlers) emailStatus(writer http.ResponseWriter, request *http.Request) {
	stats, err := handlers.growth.Email.Stats(request.Context(), handlers.growth.EmailSending)
	handlers.respond(writer, request, stats, err)
}

func (handlers *AdminHandlers) emailTest(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Address string `json:"address"`
		Locale  string `json:"locale"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	err := handlers.growth.Email.SendTest(request.Context(), body.Address, body.Locale, actorFrom(request))
	if errors.Is(err, emailpg.ErrInvalidAddress) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "email_invalid", "Enter a valid e-mail address")
		return
	}
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusAccepted)
}

func (handlers *AccountHandlers) emailUnsubscribe(writer http.ResponseWriter, request *http.Request) {
	token := request.URL.Query().Get("token")
	if token == "" && strings.HasPrefix(request.Header.Get("Content-Type"), "application/json") {
		var body struct {
			Token string `json:"token"`
		}
		if !decodeJSON(writer, request, &body) {
			return
		}
		token = body.Token
	}
	err := handlers.growth.Email.Unsubscribe(request.Context(), token)
	switch {
	case errors.Is(err, mailer.ErrBadUnsubscribeToken):
		writeProblem(writer, request, http.StatusNotFound, "unsubscribe_invalid", "That unsubscribe link is not valid")
	case err != nil:
		handlers.logger.Error("unsubscribe failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "unsubscribe_failed", "Try again later")
	default:
		writeJSON(writer, http.StatusOK, map[string]any{"unsubscribed": true})
	}
}

func (handlers *AccountHandlers) emailUnsubscribePage(writer http.ResponseWriter, request *http.Request) {
	target := "/unsubscribe?token=" + url.QueryEscape(request.URL.Query().Get("token"))
	http.Redirect(writer, request, target, http.StatusSeeOther)
}
