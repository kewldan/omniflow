package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// Every sensitive mutation is authorised.
//
// The existing route tests check a hand-written list, which proves the routes
// on that list are gated and says nothing about the one somebody added last
// week. This walks the router itself, so a new mutating route is covered the
// moment it is registered and a missing gate fails the build rather than
// waiting to be noticed.
//
// It asserts the session gate rather than the permission gate. Permissions are
// declared at the mount points and unit-tested in `internal/rbac`; what a route
// table can prove is that nothing is reachable without signing in, which is the
// property a misplaced Mount call actually breaks.

// mountedRouter builds the panel with a non-nil service, so every route is
// registered. Nothing reaches the database: the session gate refuses first.
func mountedRouter(t *testing.T) chi.Routes {
	t.Helper()
	handlers := NewAdminHandlers(AdminOptions{
		Logger:     slog.New(slog.DiscardHandler),
		Operations: &panelpg.Service{},
	})
	router := chi.NewRouter()
	handlers.Mount(router)
	return router
}

// mutatingMethods are the ones that change something.
var mutatingMethods = map[string]bool{
	http.MethodPost: true, http.MethodPut: true,
	http.MethodPatch: true, http.MethodDelete: true,
}

// openPaths are the routes that must work before anybody has a session: signing
// in, starting the OIDC flow, and the first-run setup. Each is listed
// explicitly, so adding another is a deliberate act somebody reviews.
var openPaths = map[string]bool{
	// First-run bootstrap, before any operator account exists.
	"/v1/panel/bootstrap": true,
	// Signing in, including the second factor: the challenge is part of the
	// same flow and necessarily runs before a session exists.
	"/v1/panel/auth/login":     true,
	"/v1/panel/auth/challenge": true,
	// Password reset, which is for the operator who cannot sign in.
	"/v1/panel/auth/password-reset":          true,
	"/v1/panel/auth/password-reset/complete": true,
	// Starting and completing the OIDC exchange.
	"/v1/panel/auth/oidc":          true,
	"/v1/panel/auth/oidc/callback": true,
}

// concreteFor turns a chi pattern into a path a request can be made against.
func concreteFor(pattern string) string {
	segments := strings.Split(pattern, "/")
	for index, segment := range segments {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			segments[index] = "00000000-0000-0000-0000-000000000000"
		}
	}
	return strings.TrimSuffix(strings.Join(segments, "/"), "/*")
}

func TestEveryMutatingPanelRouteRequiresASession(t *testing.T) {
	router := mountedRouter(t)

	type route struct{ method, pattern string }
	mutations := make([]route, 0, 64)
	err := chi.Walk(router, func(
		method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler,
	) error {
		if mutatingMethods[method] && strings.HasPrefix(pattern, "/v1/panel") {
			mutations = append(mutations, route{method: method, pattern: pattern})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(mutations) < 30 {
		// A walk that finds almost nothing means the router changed shape and
		// this test is now asserting something about an empty list.
		t.Fatalf("only %d mutating routes found, which suggests the walk missed them", len(mutations))
	}

	for _, candidate := range mutations {
		path := concreteFor(candidate.pattern)
		if openPaths[path] {
			continue
		}
		recorder := httptest.NewRecorder()
		router.(http.Handler).ServeHTTP(
			recorder, httptest.NewRequest(candidate.method, path, nil))

		// 401 is the gate refusing. 404 is acceptable only for a route that is
		// not mounted at all, which this walk proves is not the case here.
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s answered %d without a session, want 401",
				candidate.method, path, recorder.Code)
		}
	}
}

// The settings, marketing, and MCP surfaces were added late and are the ones
// most likely to have been mounted outside the authenticated group. Naming them
// means a failure points at the mount rather than at "some route".
func TestTheNewSurfacesAreInsideTheAuthenticatedGroup(t *testing.T) {
	router := mountedRouter(t)

	for _, candidate := range []struct{ method, path string }{
		{http.MethodGet, "/v1/panel/settings"},
		{http.MethodGet, "/v1/panel/settings/branding"},
		{http.MethodPut, "/v1/panel/settings/branding"},
		{http.MethodGet, "/v1/panel/settings/ai/providers"},
		{http.MethodPut, "/v1/panel/settings/ai/providers"},
		// The one AI route that reaches a provider. It opens a sealed credential
		// and spends money, so being outside the session gate would be the worst
		// place in this list to be wrong.
		{http.MethodPost, "/v1/panel/settings/ai/providers/acme/test"},
		{http.MethodGet, "/v1/panel/settings/ai/features"},
		{http.MethodGet, "/v1/panel/settings/ai/usage"},
		{http.MethodGet, "/v1/panel/settings/ai/decisions"},
		{http.MethodGet, "/v1/panel/settings/mcp/servers"},
		{http.MethodPut, "/v1/panel/settings/mcp/servers"},
		{http.MethodGet, "/v1/panel/settings/mcp/events"},
		{http.MethodGet, "/v1/panel/settings/diagnostics"},
		{http.MethodGet, "/v1/panel/settings/telemetry/preview"},
		{http.MethodGet, "/v1/panel/settings/backups"},
		{http.MethodGet, "/v1/panel/marketing/campaigns"},
		{http.MethodPost, "/v1/panel/marketing/campaigns"},
		{http.MethodGet, "/v1/panel/marketing/referrals"},
		{http.MethodPut, "/v1/panel/marketing/referrals"},
		{http.MethodGet, "/v1/panel/marketing/suppressions"},
	} {
		recorder := httptest.NewRecorder()
		router.(http.Handler).ServeHTTP(
			recorder, httptest.NewRequest(candidate.method, candidate.path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s answered %d without a session, want 401",
				candidate.method, candidate.path, recorder.Code)
		}
	}
}

// A surface with no service behind it is absent rather than broken, so an
// installation running only the earlier foundation gets 404 instead of a
// handler that panics on a nil pointer.
func TestTheNewSurfacesAreAbsentWithoutTheService(t *testing.T) {
	handlers := NewAdminHandlers(AdminOptions{Logger: slog.New(slog.DiscardHandler)})
	router := chi.NewRouter()
	handlers.Mount(router)

	for _, path := range []string{
		"/v1/panel/settings/ai/providers",
		"/v1/panel/settings/ai/providers/acme/test",
		"/v1/panel/settings/mcp/servers",
		"/v1/panel/marketing/campaigns",
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s answered %d without an operations service, want 404", path, recorder.Code)
		}
	}
}
