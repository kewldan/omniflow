package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/accountpg"
	"github.com/omniflow/omniflow/internal/accountreferral"
	"github.com/omniflow/omniflow/internal/accountshop"
	"github.com/omniflow/omniflow/internal/accountsupport"
	"github.com/omniflow/omniflow/internal/customerauthpg"
	"github.com/omniflow/omniflow/internal/platform"
)

// AccountHandlers serves the customer web panel API.
//
// It is mounted at /v1/account, a third prefix alongside /v1/panel (operator
// session, CSRF, RBAC) and /v1/admin (operator bearer token). The separation is
// the same rule the other two follow: each prefix carries its own middleware,
// and merging any two would mean one silently inherits the other's. A customer
// session must never be able to reach an operator route, and the cheapest way to
// guarantee that is for the two never to share a router.
type AccountHandlers struct {
	auth    *customerauthpg.Service
	account *accountpg.Service
	limiter *platform.RateLimiter
	logger  *slog.Logger
	proxies *TrustedProxies

	// The four v0.10 surfaces. Each is optional: an installation that sells no
	// digital goods, or runs with no payment provider configured, presents a
	// panel without those screens rather than one whose controls fail when
	// pressed.
	checkout *accountcheckout.Service
	shop     *accountshop.Service
	support  *accountsupport.Service
	referral *accountreferral.Service

	cookieName   string
	cookieSecure bool
	cookiePath   string
}

// AccountOptions configures the customer API.
type AccountOptions struct {
	Auth     *customerauthpg.Service
	Account  *accountpg.Service
	Checkout *accountcheckout.Service
	Shop     *accountshop.Service
	Support  *accountsupport.Service
	Referral *accountreferral.Service
	Limiter  *platform.RateLimiter
	Logger   *slog.Logger
	Proxies  *TrustedProxies
	// CookieSecure must be true in production. It is separately configurable
	// only so a plain-HTTP local development stack can sign in at all.
	CookieSecure bool
}

// NewAccountHandlers builds the customer API.
func NewAccountHandlers(options AccountOptions) *AccountHandlers {
	proxies := options.Proxies
	if proxies == nil {
		proxies = &TrustedProxies{}
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	// Session-token rotation needs a temporary store to be safe under the
	// burst of parallel requests a page load produces; the limiter already
	// holds the Valkey connection, so it is lent here rather than a second
	// one being threaded through the process wiring. Without a store the
	// identity service skips rotation instead of performing it unsafely.
	if options.Auth != nil && options.Limiter != nil {
		if store := options.Limiter.TemporaryStore(); store != nil {
			options.Auth.SetRotationGrace(store)
		}
	}
	return &AccountHandlers{
		auth: options.Auth, account: options.Account, limiter: options.Limiter,
		logger: logger, proxies: proxies,
		checkout: options.Checkout, shop: options.Shop,
		support: options.Support, referral: options.Referral,
		// The __Host- prefix binds the cookie to this exact origin, so a sibling
		// subdomain cannot set or overwrite a customer's session. It applies
		// only when the cookie is Secure, because a browser rejects a __Host-
		// cookie that is not — see cookiename.go. The name differs from the
		// operator cookie so the two panels can be open in one browser without
		// either replacing the other.
		cookieName:   cookieName(accountSessionCookieBase, options.CookieSecure),
		cookieSecure: options.CookieSecure,
		cookiePath:   "/",
	}
}

// Mount registers the customer routes.
func (handlers *AccountHandlers) Mount(router chi.Router) {
	router.Route("/v1/account", func(account chi.Router) {
		account.Use(CustomerSecurityHeaders)

		// Reachable before sign-in, by definition.
		account.Get("/auth/methods", handlers.signInMethods)
		account.Post("/auth/telegram", handlers.signInWithTelegram)
		account.Post("/auth/telegram/miniapp", handlers.signInWithMiniApp)
		account.Get("/auth/link", handlers.completeMagicLink)
		account.Get("/auth/oidc/{slug}/start", handlers.startOIDC)
		account.Get("/auth/oidc/{slug}/callback", handlers.callbackOIDC)

		account.Group(func(secure chi.Router) {
			secure.Use(handlers.requireCustomerSession)
			secure.Use(handlers.requireCustomerCSRF)

			secure.Get("/me", handlers.currentAccount)
			secure.Patch("/me", handlers.updateProfile)
			secure.Post("/auth/logout", handlers.logout)
			secure.Post("/auth/logout-all", handlers.logoutEverywhere)

			secure.Get("/sessions", handlers.listSessions)
			secure.Delete("/sessions/{sessionID}", handlers.revokeSession)
			secure.Get("/security-events", handlers.listSecurityEvents)

			secure.Get("/sign-in-methods", handlers.listSignInMethods)
			// Unlinking is one of the actions that needs a recent sign-in: a
			// session left open on a shared machine must not be enough to strip
			// somebody's way back into their own account.
			secure.With(handlers.requireRecentAuthentication).
				Delete("/sign-in-methods/{identityID}", handlers.unlinkSignInMethod)
			secure.Get("/sign-in-methods/oidc/{slug}/start", handlers.startOIDCLink)

			secure.Get("/overview", handlers.overview)
			secure.Get("/subscriptions/{subscriptionID}", handlers.subscription)
			secure.Patch("/subscriptions/{subscriptionID}", handlers.renameSubscription)
			secure.Get("/subscriptions/{subscriptionID}/connection", handlers.connection)
			secure.With(handlers.requireRecentAuthentication).
				Post("/subscriptions/{subscriptionID}/rotate-link", handlers.rotateSubscriptionLink)

			secure.Get("/subscriptions/{subscriptionID}/devices", handlers.listDevices)
			secure.Delete("/subscriptions/{subscriptionID}/devices/{handle}", handlers.removeDevice)
			secure.With(handlers.requireRecentAuthentication).
				Delete("/subscriptions/{subscriptionID}/devices", handlers.removeAllDevices)

			// The v0.10 surfaces. Each mounts itself only when its service is
			// attached, so the route table describes what this installation can
			// actually do rather than what the binary was compiled with.
			handlers.mountCheckout(secure)
			handlers.mountShop(secure)
			handlers.mountSupport(secure)
			handlers.mountReferral(secure)
		})
	})
}

// ready reports whether an identity service is attached, answering 503 when it
// is not.
//
// Only the routes reachable before sign-in need this: requireCustomerSession
// refuses everything behind it before any handler runs.
func (handlers *AccountHandlers) ready(writer http.ResponseWriter, request *http.Request) bool {
	if handlers.auth == nil {
		writeProblem(
			writer, request, http.StatusServiceUnavailable,
			"account_unavailable", "The customer panel is not configured",
		)
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// Cookies
// ---------------------------------------------------------------------------

func (handlers *AccountHandlers) setSessionCookie(
	writer http.ResponseWriter, token string, expires time.Time,
) {
	http.SetCookie(writer, &http.Cookie{
		Name:     handlers.cookieName,
		Value:    token,
		Path:     handlers.cookiePath,
		Expires:  expires.UTC(),
		HttpOnly: true,
		Secure:   handlers.cookieSecure,
		// Lax rather than Strict so a magic link or an OIDC callback — both of
		// which arrive as cross-site navigations — lands signed in. The CSRF
		// token, not the cookie policy, is what defends the unsafe methods.
		SameSite: http.SameSiteLaxMode,
	})
}

func (handlers *AccountHandlers) clearSessionCookie(writer http.ResponseWriter) {
	http.SetCookie(writer, &http.Cookie{
		Name:     handlers.cookieName,
		Value:    "",
		Path:     handlers.cookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   handlers.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}
