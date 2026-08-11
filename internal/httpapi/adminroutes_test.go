package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// newTestRouter mounts the panel with no service attached. Every route that
// reaches a handler will fail once it touches the database, so these tests
// assert only on routing and on the gates in front of it — which is exactly
// what a misplaced Mount call would break.
func newTestRouter() http.Handler {
	handlers := NewAdminHandlers(AdminOptions{Logger: slog.New(slog.DiscardHandler)})
	router := chi.NewRouter()
	handlers.Mount(router)
	return router
}

// The sign-in half of OIDC runs before anyone is authenticated. If it were
// registered inside the authenticated group it would answer 401 and no operator
// could ever start the flow.
func TestOIDCSignInRoutesAreReachableWithoutASession(t *testing.T) {
	router := newTestRouter()
	for _, path := range []string{
		"/v1/panel/auth/oidc",
		"/v1/panel/auth/oidc/acme/start",
		"/v1/panel/auth/oidc/acme/callback",
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code == http.StatusNotFound {
			t.Fatalf("%s is not routed", path)
		}
		if recorder.Code == http.StatusUnauthorized {
			t.Fatalf("%s requires a session; the sign-in flow can never start", path)
		}
	}
}

// Provider configuration is an installation setting and must sit behind the
// session gate, unlike the sign-in half above.
func TestOIDCConfigurationRequiresASession(t *testing.T) {
	router := newTestRouter()
	cases := []struct{ method, path string }{
		{http.MethodGet, "/v1/panel/settings/oidc"},
		{http.MethodPut, "/v1/panel/settings/oidc"},
		{http.MethodDelete, "/v1/panel/settings/oidc/acme"},
	}
	for _, testCase := range cases {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(testCase.method, testCase.path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s answered %d, want 401", testCase.method, testCase.path, recorder.Code)
		}
	}
}

func TestPanelRoutesRequireASession(t *testing.T) {
	router := newTestRouter()
	cases := []struct{ method, path string }{
		{http.MethodGet, "/v1/panel/auth/session"},
		{http.MethodGet, "/v1/panel/auth/sessions"},
		{http.MethodGet, "/v1/panel/admins"},
		{http.MethodPost, "/v1/panel/admins"},
		{http.MethodGet, "/v1/panel/audit"},
		{http.MethodGet, "/v1/panel/audit/export"},
		{http.MethodPut, "/v1/panel/auth/preferences"},
		{http.MethodPost, "/v1/panel/auth/totp"},
	}
	for _, testCase := range cases {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(testCase.method, testCase.path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s answered %d, want 401", testCase.method, testCase.path, recorder.Code)
		}
	}
}

// The operations routes are mounted only when an operations service is
// attached. A panel running the v0.6 foundation alone must answer 404 rather
// than reaching a nil service and panicking on the first request.
func TestOperationsRoutesAreAbsentWithoutTheService(t *testing.T) {
	router := newTestRouter()
	for _, path := range []string{
		"/v1/panel/overview/dashboard",
		"/v1/panel/customers",
		"/v1/panel/finance/orders",
		"/v1/panel/goods/products",
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s answered %d without an operations service, want 404", path, recorder.Code)
		}
	}
}

// Every operations route sits behind the session gate. A route that answered
// anything but 401 unauthenticated would be reachable by anyone who knows the
// path, whatever permission it declares.
func TestOperationsRoutesRequireASession(t *testing.T) {
	handlers := NewAdminHandlers(AdminOptions{
		Logger: slog.New(slog.DiscardHandler),
		// A non-nil service is enough to mount the routes; nothing in this test
		// reaches the database, because the session gate refuses first.
		Operations: &panelpg.Service{},
	})
	router := chi.NewRouter()
	handlers.Mount(router)

	cases := []struct{ method, path string }{
		{http.MethodGet, "/v1/panel/overview/dashboard"},
		{http.MethodGet, "/v1/panel/customers"},
		{http.MethodGet, "/v1/panel/customers/00000000-0000-0000-0000-000000000000"},
		{http.MethodPost, "/v1/panel/customers/00000000-0000-0000-0000-000000000000/status"},
		{http.MethodGet, "/v1/panel/catalog/plans"},
		{http.MethodGet, "/v1/panel/finance/orders"},
		{http.MethodGet, "/v1/panel/finance/export"},
		{http.MethodGet, "/v1/panel/system/jobs"},
		{http.MethodPost, "/v1/panel/system/webhooks/00000000-0000-0000-0000-000000000000/replay"},
		{http.MethodGet, "/v1/panel/settings/commerce"},
		{http.MethodPut, "/v1/panel/settings/commerce/topup"},
		{http.MethodGet, "/v1/panel/risk/matches"},
		{http.MethodPost, "/v1/panel/risk/anomalies/00000000-0000-0000-0000-000000000000/review"},
		{http.MethodGet, "/v1/panel/gifts"},
		{http.MethodGet, "/v1/panel/goods/products"},
		{http.MethodPost, "/v1/panel/bulk"},
	}
	for _, testCase := range cases {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(testCase.method, testCase.path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s answered %d, want 401", testCase.method, testCase.path, recorder.Code)
		}
	}
}

// Bootstrap and sign-in must stay reachable, or a fresh installation could
// never create its first owner.
func TestUnauthenticatedRoutesStayOpen(t *testing.T) {
	router := newTestRouter()
	for _, path := range []string{"/v1/panel/bootstrap"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code == http.StatusUnauthorized || recorder.Code == http.StatusNotFound {
			t.Fatalf("%s answered %d; it must be reachable before sign-in", path, recorder.Code)
		}
	}
}

// Every panel response carries the restrictive headers, including the ones
// served before authentication.
func TestPanelResponsesCarrySecurityHeaders(t *testing.T) {
	router := newTestRouter()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/panel/auth/session", nil))
	if got := recorder.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options = %q, want DENY", got)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}
