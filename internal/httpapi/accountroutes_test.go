package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// newAccountRouter mounts the customer surface with no services attached. Every
// route that reaches a handler fails once it touches the database, so these
// tests assert only on routing and on the gates in front of it — which is
// exactly what a misplaced Mount call would break.
func newAccountRouter() http.Handler {
	handlers := NewAccountHandlers(AccountOptions{Logger: slog.New(slog.DiscardHandler)})
	router := chi.NewRouter()
	handlers.Mount(router)
	return router
}

// The sign-in routes run before anyone is authenticated. Registered inside the
// authenticated group they would answer 401 and no customer could ever sign in.
func TestAccountSignInRoutesAreReachableWithoutASession(t *testing.T) {
	router := newAccountRouter()
	cases := []struct{ method, path string }{
		{http.MethodGet, "/v1/account/auth/methods"},
		{http.MethodPost, "/v1/account/auth/telegram"},
		{http.MethodPost, "/v1/account/auth/telegram/miniapp"},
		{http.MethodGet, "/v1/account/auth/link?token=x"},
		{http.MethodGet, "/v1/account/auth/oidc/acme/start"},
		{http.MethodGet, "/v1/account/auth/oidc/acme/callback"},
	}
	for _, testCase := range cases {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(testCase.method, testCase.path, nil))
		if recorder.Code == http.StatusNotFound {
			t.Fatalf("%s %s is not routed", testCase.method, testCase.path)
		}
		if recorder.Code == http.StatusUnauthorized {
			t.Fatalf("%s %s requires a session; sign-in could never start", testCase.method, testCase.path)
		}
	}
}

func TestAccountRoutesRequireASession(t *testing.T) {
	router := newAccountRouter()
	const subscription = "/v1/account/subscriptions/2f1c0c2e-0000-4000-8000-000000000000"
	cases := []struct{ method, path string }{
		{http.MethodGet, "/v1/account/me"},
		{http.MethodPatch, "/v1/account/me"},
		{http.MethodPost, "/v1/account/auth/logout"},
		{http.MethodPost, "/v1/account/auth/logout-all"},
		{http.MethodGet, "/v1/account/sessions"},
		{http.MethodDelete, "/v1/account/sessions/2f1c0c2e-0000-4000-8000-000000000000"},
		{http.MethodGet, "/v1/account/security-events"},
		{http.MethodGet, "/v1/account/sign-in-methods"},
		{http.MethodDelete, "/v1/account/sign-in-methods/2f1c0c2e-0000-4000-8000-000000000000"},
		{http.MethodGet, "/v1/account/sign-in-methods/oidc/acme/start"},
		{http.MethodGet, "/v1/account/overview"},
		{http.MethodGet, subscription},
		{http.MethodPatch, subscription},
		{http.MethodGet, subscription + "/connection"},
		{http.MethodPost, subscription + "/rotate-link"},
		{http.MethodGet, subscription + "/devices"},
		{http.MethodDelete, subscription + "/devices?confirm=true"},
		{http.MethodDelete, subscription + "/devices/abc"},
	}
	for _, testCase := range cases {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(testCase.method, testCase.path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s answered %d, want 401", testCase.method, testCase.path, recorder.Code)
		}
	}
}

// A customer session must never be able to reach an operator route. The two
// prefixes carry different middleware, and merging them would silently give one
// the other's gate.
func TestAccountAndPanelPrefixesDoNotOverlap(t *testing.T) {
	router := newAccountRouter()
	for _, path := range []string{"/v1/panel/auth/session", "/v1/admin/orders", "/v1/panel/admins"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s answered %d from the customer router; the prefixes overlap", path, recorder.Code)
		}
	}
}

// A router built without a customer service must report the surface as
// unavailable rather than panicking on the first anonymous request.
func TestAccountSurfaceDegradesWithoutAService(t *testing.T) {
	router := newAccountRouter()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/account/auth/methods", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("answered %d, want 503", recorder.Code)
	}
}

// The customer surface is absent entirely when the process does not attach it,
// which is what a Telegram-only installation runs.
func TestAccountSurfaceIsAbsentWhenNotMounted(t *testing.T) {
	router := NewRouter(slog.New(slog.DiscardHandler), RouterOptions{})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/account/me", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("answered %d, want 404", recorder.Code)
	}
}

// A customer response can carry a subscription link, which is a credential.
// `no-store` keeps it out of shared caches and the back-forward cache, and
// `no-referrer` keeps a URL carrying one from being handed to the next
// navigation. Both are enforced at the transport rather than per screen.
func TestAccountResponsesAreNotCachedOrReferred(t *testing.T) {
	router := newAccountRouter()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/account/auth/methods", nil))

	if got := recorder.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := recorder.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q, want no-referrer", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := recorder.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
		t.Fatalf("Content-Security-Policy = %q, want frame-ancestors 'none'", got)
	}
}

// Customer sign-in providers are an installation setting on the operator panel.
// Without a customer identity service attached the routes are absent rather than
// answering with a half-configured surface.
func TestCustomerProviderSettingsAreAbsentWithoutTheService(t *testing.T) {
	handlers := NewAdminHandlers(AdminOptions{Logger: slog.New(slog.DiscardHandler)})
	router := chi.NewRouter()
	handlers.Mount(router)

	for _, path := range []string{
		"/v1/panel/settings/customer-oidc",
		"/v1/panel/settings/customer-oidc/presets",
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s answered %d, want 404", path, recorder.Code)
		}
	}
}
