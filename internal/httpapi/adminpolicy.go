package httpapi

import (
	"net/http"
	"strings"
)

// secondFactorExempt are the routes an operator without a second factor can
// still reach when the installation requires one: their own session, the
// sign-out, and everything needed to enrol an authenticator or a passkey.
var secondFactorExempt = []string{
	"/v1/panel/auth/", "/v1/panel/passkeys", "/v1/panel/rbac/catalog",
}

// requireSecondFactorPolicy enforces the security section's requireTotp
// switch. With it on, an operator who has neither an authenticator app nor a
// passkey may only manage their own sign-in until they enrol one; every other
// route answers 403 second_factor_required, which the panel turns into a
// pointer to the security screen. It is enforced here, server side, because a
// redirect in the panel alone would leave the API open to the same session.
func (handlers *AdminHandlers) requireSecondFactorPolicy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if handlers.installation == nil ||
			!handlers.installation.Section(request.Context(), "security").Bool("requireTotp", false) {
			next.ServeHTTP(writer, request)
			return
		}
		for _, prefix := range secondFactorExempt {
			if strings.HasPrefix(request.URL.Path, prefix) {
				next.ServeHTTP(writer, request)
				return
			}
		}
		principal, ok := PrincipalFrom(request.Context())
		if !ok || principal.Account.TOTPEnabled {
			next.ServeHTTP(writer, request)
			return
		}
		if keys, err := handlers.service.Passkeys(request.Context(), principal.Account.ID); err == nil && len(keys) > 0 {
			next.ServeHTTP(writer, request)
			return
		}
		writeProblem(writer, request, http.StatusForbidden, "second_factor_required",
			"This installation requires a second factor. Add an authenticator app or a passkey first")
	})
}
