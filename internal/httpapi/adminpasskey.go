package httpapi

import (
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/omniflow/omniflow/internal/adminauth"
	"github.com/omniflow/omniflow/internal/adminauthpg"
)

// Passkeys.
//
// A passkey signs an operator in on its own, so half of this is public by
// necessity: the challenge and the assertion that answers it both happen before
// anyone is authenticated. The other half — registering and revoking a key —
// belongs to the operator who owns it and sits behind the session gate.
//
// The WebAuthn exchange is two requests, and the challenge issued by the first
// must reach the second unaltered. It travels in a short-lived, sealed,
// HttpOnly cookie, exactly as the OIDC flow state does: a challenge the browser
// could choose is not a challenge.

// passkeyFlowCookie carries the WebAuthn session between the two steps.
func (handlers *AdminHandlers) passkeyFlowCookie() string {
	return cookieName(adminPasskeyCookieBase, handlers.cookieSecure)
}

// passkeyFlowTTL bounds how long a challenge stays answerable. A person picking
// up a security key has a minute or two; anything longer is a challenge sitting
// in a browser waiting to be replayed.
const passkeyFlowTTL = 5 * time.Minute

// mountPasskeyPublic registers the sign-in half, which by definition runs
// before anybody is authenticated.
func (handlers *AdminHandlers) mountPasskeyPublic(panel chi.Router) {
	panel.Get("/auth/passkey", handlers.passkeyAvailability)
	panel.Post("/auth/passkey/login/begin", handlers.beginPasskeyLogin)
	panel.Post("/auth/passkey/login/finish", handlers.finishPasskeyLogin)
}

// mountPasskeySecure registers key management, which belongs to its owner.
func (handlers *AdminHandlers) mountPasskeySecure(secure chi.Router) {
	secure.Get("/auth/passkeys", handlers.listPasskeys)
	secure.Post("/auth/passkeys/register/begin", handlers.beginPasskeyRegistration)
	secure.Post("/auth/passkeys/register/finish", handlers.finishPasskeyRegistration)
	secure.Patch("/auth/passkeys/{passkeyID}", handlers.renamePasskey)
	secure.Delete("/auth/passkeys/{passkeyID}", handlers.deletePasskey)
}

// passkeyAvailability says whether the sign-in screen should offer the button.
//
// Passkeys need an origin to bind to, and an installation without a public URL
// has none. Answering here rather than letting the button fail is the
// difference between "not offered" and "offered and broken".
func (handlers *AdminHandlers) passkeyAvailability(writer http.ResponseWriter, request *http.Request) {
	if !handlers.ready(writer, request) {
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"available": handlers.service.PasskeysEnabled(),
	})
}

func (handlers *AdminHandlers) beginPasskeyLogin(writer http.ResponseWriter, request *http.Request) {
	if !handlers.ready(writer, request) {
		return
	}
	// The challenge is cheap to mint and answering it is not, but an unbounded
	// endpoint is still a way to make the server do work for free.
	if !handlers.allow(request, "admin-passkey", clientKey(handlers.requestContext(request)), 30, time.Hour) {
		writeProblem(writer, request, http.StatusTooManyRequests, "rate_limited", "Too many attempts. Try again shortly")
		return
	}

	challenge, err := handlers.service.BeginPasskeyLogin()
	if handlers.passkeyUnavailable(writer, request, err) {
		return
	}
	if err != nil {
		handlers.logger.Error("passkey challenge failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "login_unavailable", "Sign-in is unavailable")
		return
	}
	if !handlers.setPasskeyFlow(writer, request, challenge.State) {
		return
	}
	writeJSON(writer, http.StatusOK, challenge.Options)
}

func (handlers *AdminHandlers) finishPasskeyLogin(writer http.ResponseWriter, request *http.Request) {
	if !handlers.ready(writer, request) {
		return
	}
	state, ok := handlers.passkeyFlow(writer, request)
	if !ok {
		return
	}
	// The challenge is single-use whatever happens next: a failed assertion
	// must not leave one behind for a second attempt to reuse.
	handlers.clearPasskeyFlow(writer)

	parsed, err := protocol.ParseCredentialRequestResponseBody(request.Body)
	if err != nil {
		writeProblem(writer, request, http.StatusBadRequest, "invalid_request", "That is not a valid assertion")
		return
	}

	result, err := handlers.service.FinishPasskeyLogin(
		request.Context(), state, parsed, handlers.requestContext(request))
	switch {
	case handlers.passkeyUnavailable(writer, request, err):
		return
	case errors.Is(err, adminauth.ErrPasskeyCloned):
		// Named rather than folded into the generic refusal: the person holding
		// the original key needs to know a copy answered, and a screen that
		// said only "that did not work" would never tell them.
		writeProblem(
			writer, request, http.StatusUnauthorized, "passkey_cloned",
			"That passkey was refused because another device answered for it. The account has been notified",
		)
		return
	case errors.Is(err, adminauthpg.ErrInvalidCredentials),
		errors.Is(err, adminauth.ErrPasskeyUnverified),
		errors.Is(err, adminauthpg.ErrAccountDisabled):
		// One answer for an unknown credential, a bad signature, and a disabled
		// account, for the same reason the password path has one.
		writeProblem(writer, request, http.StatusUnauthorized, "invalid_credentials", "That passkey was not accepted")
		return
	case err != nil:
		handlers.logger.Error("passkey sign-in failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "login_unavailable", "Sign-in is unavailable")
		return
	}

	handlers.setSessionCookie(writer, result.Token, result.ExpiresAt)
	writeJSON(writer, http.StatusOK, map[string]any{
		// Always false. A passkey proved possession and verified the person, so
		// there is no second factor left to demand; the field is present so the
		// sign-in screen can treat both routes identically.
		"challengeRequired": false,
		"csrfToken":         result.CSRFToken,
		"expiresAt":         result.ExpiresAt.UTC(),
		"account":           accountPayload(result.Account),
	})
}

func (handlers *AdminHandlers) listPasskeys(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	keys, err := handlers.service.Passkeys(request.Context(), principal.Account.ID)
	if err != nil {
		handlers.logger.Error("passkey listing failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "unavailable", "That could not be read")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"items": keys, "available": handlers.service.PasskeysEnabled(),
	})
}

func (handlers *AdminHandlers) beginPasskeyRegistration(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	challenge, err := handlers.service.BeginPasskeyRegistration(request.Context(), principal.Account.ID)
	if handlers.passkeyUnavailable(writer, request, err) {
		return
	}
	if err != nil {
		handlers.logger.Error("passkey registration challenge failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "unavailable", "That could not be started")
		return
	}
	if !handlers.setPasskeyFlow(writer, request, challenge.State) {
		return
	}
	writeJSON(writer, http.StatusOK, challenge.Options)
}

func (handlers *AdminHandlers) finishPasskeyRegistration(writer http.ResponseWriter, request *http.Request) {
	state, ok := handlers.passkeyFlow(writer, request)
	if !ok {
		return
	}
	handlers.clearPasskeyFlow(writer)

	// The label travels as a query parameter because the body is the credential
	// and belongs to the parser: WebAuthn's response shape is fixed, and
	// wrapping it to carry one extra string would mean unwrapping it here.
	label := query(request, "label")
	parsed, err := protocol.ParseCredentialCreationResponseBody(request.Body)
	if err != nil {
		writeProblem(writer, request, http.StatusBadRequest, "invalid_request", "That is not a valid credential")
		return
	}

	principal, _ := PrincipalFrom(request.Context())
	key, err := handlers.service.FinishPasskeyRegistration(
		request.Context(), principal.Account.ID, label, state, parsed,
		handlers.requestContext(request))
	switch {
	case handlers.passkeyUnavailable(writer, request, err):
		return
	case errors.Is(err, adminauth.ErrPasskeyUnverified):
		writeProblem(
			writer, request, http.StatusUnprocessableEntity, "passkey_unverified",
			"That authenticator did not verify who was using it, so it cannot sign in on its own",
		)
		return
	case errors.Is(err, adminauthpg.ErrInvalidCredentials):
		writeProblem(writer, request, http.StatusBadRequest, "invalid_request", "That credential was not accepted")
		return
	case err != nil:
		handlers.logger.Error("passkey registration failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "unavailable", "That could not be saved")
		return
	}
	writeJSON(writer, http.StatusOK, key)
}

func (handlers *AdminHandlers) renamePasskey(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Label string `json:"label"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	principal, _ := PrincipalFrom(request.Context())
	key, err := handlers.service.RenamePasskey(
		request.Context(), principal.Account.ID, chi.URLParam(request, "passkeyID"),
		body.Label, handlers.requestContext(request))
	if errors.Is(err, adminauthpg.ErrNotFound) {
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That passkey does not exist")
		return
	}
	if err != nil {
		handlers.logger.Error("passkey rename failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "unavailable", "That could not be saved")
		return
	}
	writeJSON(writer, http.StatusOK, key)
}

func (handlers *AdminHandlers) deletePasskey(writer http.ResponseWriter, request *http.Request) {
	principal, _ := PrincipalFrom(request.Context())
	err := handlers.service.DeletePasskey(
		request.Context(), principal.Account.ID, chi.URLParam(request, "passkeyID"),
		handlers.requestContext(request))
	if errors.Is(err, adminauthpg.ErrNotFound) {
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That passkey does not exist")
		return
	}
	if err != nil {
		handlers.logger.Error("passkey revocation failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "unavailable", "That could not be removed")
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// passkeyUnavailable answers the one configuration error that is not a fault.
func (handlers *AdminHandlers) passkeyUnavailable(
	writer http.ResponseWriter, request *http.Request, err error,
) bool {
	if !errors.Is(err, adminauthpg.ErrPasskeysUnavailable) {
		return false
	}
	writeProblem(
		writer, request, http.StatusServiceUnavailable, "passkeys_unavailable",
		"Passkeys need APP_PUBLIC_URL, which this installation has not set",
	)
	return true
}

func (handlers *AdminHandlers) setPasskeyFlow(
	writer http.ResponseWriter, request *http.Request, state []byte,
) bool {
	sealed, err := handlers.service.SealFlowState(state)
	if err != nil {
		handlers.logger.Error("passkey flow could not be sealed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "unavailable", "That could not be started")
		return false
	}
	http.SetCookie(writer, &http.Cookie{
		Name: handlers.passkeyFlowCookie(), Value: base64.RawURLEncoding.EncodeToString(sealed),
		Path: "/", MaxAge: int(passkeyFlowTTL.Seconds()),
		HttpOnly: true, Secure: handlers.cookieSecure,
		// Strict rather than Lax: unlike an OIDC callback, both halves of this
		// exchange are same-site fetches from a page the operator is already
		// on, so there is no cross-site navigation that needs the cookie.
		SameSite: http.SameSiteStrictMode,
	})
	return true
}

func (handlers *AdminHandlers) passkeyFlow(
	writer http.ResponseWriter, request *http.Request,
) ([]byte, bool) {
	cookie, err := request.Cookie(handlers.passkeyFlowCookie())
	if err != nil || cookie.Value == "" {
		writeProblem(writer, request, http.StatusBadRequest, "flow_expired", "That took too long. Try again")
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		writeProblem(writer, request, http.StatusBadRequest, "flow_expired", "That took too long. Try again")
		return nil, false
	}
	opened, err := handlers.service.OpenFlowState(raw)
	if err != nil {
		writeProblem(writer, request, http.StatusBadRequest, "flow_expired", "That took too long. Try again")
		return nil, false
	}
	return opened, true
}

func (handlers *AdminHandlers) clearPasskeyFlow(writer http.ResponseWriter) {
	http.SetCookie(writer, &http.Cookie{
		Name: handlers.passkeyFlowCookie(), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: handlers.cookieSecure, SameSite: http.SameSiteStrictMode,
	})
}
