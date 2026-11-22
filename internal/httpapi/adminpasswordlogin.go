package httpapi

import (
	"errors"
	"net/http"

	"github.com/omniflow/omniflow/internal/adminauthpg"
)

// setPasswordLogin turns password sign-in on or off for the operator's own
// account. Off needs a passkey of theirs that has already signed in; the way
// back without one is the api's `admin password-login` command on the server.
func (handlers *AdminHandlers) setPasswordLogin(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	account, err := handlers.service.SetPasswordLogin(
		request.Context(), principal.Account.ID, body.Enabled, handlers.requestContext(request),
	)
	switch {
	case errors.Is(err, adminauthpg.ErrNoWorkingPasskey):
		writeProblem(writer, request, http.StatusConflict, "no_working_passkey",
			"Sign in with a passkey once before turning password sign-in off")
		return
	case err != nil:
		handlers.logger.Error("password sign-in switch failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "unavailable", "Unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, accountPayload(account))
}
