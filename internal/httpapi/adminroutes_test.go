package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
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
