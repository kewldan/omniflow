package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/omniflow/omniflow/internal/accountpg"
	"github.com/omniflow/omniflow/internal/customerauthpg"
)

// customerContextKey carries the resolved customer through a request.
type customerContextKey struct{}

// CustomerFrom returns the authenticated customer, if the request passed through
// requireCustomerSession.
func CustomerFrom(ctx context.Context) (customerauthpg.Principal, bool) {
	principal, ok := ctx.Value(customerContextKey{}).(customerauthpg.Principal)
	return principal, ok
}

// CustomerSecurityHeaders applies the default headers every customer response
// carries.
//
// It differs from the operator panel's in one respect that matters. A customer
// response can contain a subscription link, which is a credential: `no-store`
// keeps it out of shared caches and the browser's back-forward cache, and
// `Referrer-Policy: no-referrer` keeps a URL that carries one from being handed
// to whatever the customer navigates to next. Those two are the "no accidental
// preview leakage" requirement, enforced at the transport rather than left to
// each screen to remember.
func CustomerSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		header := writer.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("Cross-Origin-Opener-Policy", "same-origin")
		header.Set("Cross-Origin-Resource-Policy", "same-origin")
		header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")
		header.Set(
			"Content-Security-Policy",
			"default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'",
		)
		header.Set("Cache-Control", "no-store")
		next.ServeHTTP(writer, request)
	})
}

// requestContext gathers the transport detail recorded against sessions and
// security events.
func (handlers *AccountHandlers) requestContext(request *http.Request) customerauthpg.RequestContext {
	return customerauthpg.RequestContext{
		IP:             handlers.proxies.ClientIP(request),
		UserAgent:      request.UserAgent(),
		RequestID:      middleware.GetReqID(request.Context()),
		AcceptLanguage: request.Header.Get("Accept-Language"),
	}
}

// securityRequest is the same detail in the shape the account service takes.
func (handlers *AccountHandlers) securityRequest(request *http.Request) accountpg.SecurityRequest {
	context := handlers.requestContext(request)
	address := ""
	if context.IP != nil {
		address = context.IP.String()
	}
	return accountpg.SecurityRequest{
		IP: address, UserAgent: context.UserAgent, RequestID: context.RequestID,
	}
}

// requireCustomerSession resolves the session cookie into a principal.
//
// Token rotation is applied here rather than in each handler, so a rotated
// cookie is invisible to the routes above it.
func (handlers *AccountHandlers) requireCustomerSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// The cookie is checked before the service is: a visitor presenting no
		// credential is unauthenticated whether or not the panel is configured,
		// and answering 503 here would tell an anonymous caller something about
		// the installation's configuration for no benefit.
		cookie, err := request.Cookie(handlers.cookieName)
		if err != nil || cookie.Value == "" {
			writeProblem(writer, request, http.StatusUnauthorized, "unauthenticated", "Sign-in is required")
			return
		}
		if handlers.auth == nil {
			writeProblem(
				writer, request, http.StatusServiceUnavailable,
				"account_unavailable", "The customer panel is not configured",
			)
			return
		}

		principal, err := handlers.auth.Resolve(request.Context(), cookie.Value)
		switch {
		case errors.Is(err, customerauthpg.ErrSessionInvalid):
			// The cookie is cleared so a browser holding a dead session stops
			// presenting it on every subsequent request. The service answers
			// this only when it positively knows the token is dead: a token
			// that rotated away moments ago resolves through the grace path
			// and arrives here as a principal with a replacement token, and a
			// grace store that could not be asked is the generic error below,
			// which leaves the cookie alone.
			handlers.clearSessionCookie(writer)
			writeProblem(writer, request, http.StatusUnauthorized, "unauthenticated", "Sign-in is required")
			return
		case errors.Is(err, customerauthpg.ErrAccountInactive):
			// A suspended or deleted account is a distinct state the panel
			// explains, rather than a sign-in prompt that will never succeed.
			handlers.clearSessionCookie(writer)
			writeProblem(
				writer, request, http.StatusForbidden,
				"account_unavailable", "This account is not available",
			)
			return
		case err != nil:
			handlers.logger.Error("customer session lookup failed", "error", err)
			writeProblem(
				writer, request, http.StatusInternalServerError,
				"session_unavailable", "Session lookup failed",
			)
			return
		}

		if principal.RotatedToken != "" {
			handlers.setSessionCookie(writer, principal.RotatedToken, principal.ExpiresAt)
		}
		// The panel reads this to attach the token to unsafe requests.
		writer.Header().Set("X-CSRF-Token", principal.CSRFToken)

		ctx := context.WithValue(request.Context(), customerContextKey{}, principal)
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

// requireCustomerCSRF enforces the double-submit token on every state-changing
// request.
//
// Safe methods are exempt because they change nothing. SameSite=Lax already
// blocks most cross-site form posts, but it is a browser-side control only, so
// the server verifies the token itself rather than relying on it.
func (handlers *AccountHandlers) requireCustomerCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(writer, request)
			return
		}
		principal, ok := CustomerFrom(request.Context())
		if !ok {
			writeProblem(writer, request, http.StatusUnauthorized, "unauthenticated", "Sign-in is required")
			return
		}
		submitted := request.Header.Get("X-CSRF-Token")
		if submitted == "" || submitted != principal.CSRFToken {
			writeProblem(writer, request, http.StatusForbidden, "csrf_failed", "CSRF token is missing or invalid")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

// requireRecentAuthentication gates the actions that can lock a customer out of
// their own subscription.
//
// It answers 401 with a distinct code rather than 403, because the remedy is
// something the customer can actually do — sign in again — and the panel needs to
// tell them that instead of showing a flat refusal.
func (handlers *AccountHandlers) requireRecentAuthentication(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		principal, ok := CustomerFrom(request.Context())
		if !ok {
			writeProblem(writer, request, http.StatusUnauthorized, "unauthenticated", "Sign-in is required")
			return
		}
		if principal.ReauthenticationRequired {
			writeProblem(
				writer, request, http.StatusUnauthorized,
				"reauthentication_required", "Sign in again to confirm this action",
			)
			return
		}
		next.ServeHTTP(writer, request)
	})
}
