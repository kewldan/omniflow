package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/fulfillment"
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
		// Attached so the walk below reaches the subscription routes too. They
		// mount only when a fulfillment service exists, and without one the
		// most consequential mutations in the panel — the ones that change a
		// customer's access — would be absent from the very test that exists to
		// find an ungated mutation. Nothing is called: the session gate refuses
		// before any handler runs.
		Fulfillment: &fulfillment.Service{},
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
	// Passkey sign-in. The assertion is what proves who the operator is, so
	// both halves necessarily run before a session exists. Registering a
	// passkey is a different thing entirely and stays behind the gate.
	"/v1/panel/auth/passkey/login/begin":  true,
	"/v1/panel/auth/passkey/login/finish": true,
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
		// The palette is the one settings surface with a public half, so a
		// mistake here is a mistake about which half is which.
		{http.MethodGet, "/v1/panel/settings/theme"},
		{http.MethodPut, "/v1/panel/settings/theme"},
		{http.MethodGet, "/v1/panel/settings/theme/assets"},
		{http.MethodPut, "/v1/panel/settings/theme/assets/logo_light"},
		{http.MethodDelete, "/v1/panel/settings/theme/assets/logo_light"},
		// The connection catalogue decides what a customer is told to install
		// on their own device, which makes an ungated write here a way to
		// recommend somebody else's software to every customer at once.
		{http.MethodGet, "/v1/panel/settings/connect"},
		{http.MethodPut, "/v1/panel/settings/connect/platforms"},
		{http.MethodDelete, "/v1/panel/settings/connect/platforms/ios"},
		{http.MethodPut, "/v1/panel/settings/connect/clients"},
		// Reporting is money, so it sits behind finance.read; being outside the
		// session gate would publish every figure an installation has.
		{http.MethodGet, "/v1/panel/reports/sales"},
		{http.MethodGet, "/v1/panel/reports/sales/export"},
		{http.MethodGet, "/v1/panel/reports/payments"},
		{http.MethodGet, "/v1/panel/reports/traffic"},
		{http.MethodGet, "/v1/panel/reports/traffic/export"},
		// Information pages are published to the world, so writing one has to be
		// as gated as any other mutation even though reading one is not gated at
		// all.
		{http.MethodGet, "/v1/panel/content/pages"},
		{http.MethodPut, "/v1/panel/content/pages"},
		{http.MethodPost, "/v1/panel/content/pages/terms/publication"},
		{http.MethodDelete, "/v1/panel/content/pages/terms"},
		// Generating a batch is the one call in the API that returns redeemable
		// codes, so being outside the session gate would hand out subscriptions.
		{http.MethodGet, "/v1/panel/codes/batches"},
		{http.MethodPost, "/v1/panel/codes/batches"},
		// A merge combines two people's records irreversibly.
		{http.MethodGet, "/v1/panel/customers/00000000-0000-0000-0000-000000000000/merge/preview"},
		{http.MethodPost, "/v1/panel/customers/00000000-0000-0000-0000-000000000000/merge"},
		// A counter identifier lands in every visitor's browser, and the
		// conversion export is revenue data.
		{http.MethodGet, "/v1/panel/analytics"},
		{http.MethodPut, "/v1/panel/analytics"},
		{http.MethodGet, "/v1/panel/reports/channels"},
		{http.MethodGet, "/v1/panel/reports/conversions"},
		// Notice wording reaches every customer of the installation, and the
		// preview and test routes run a renderer over caller-supplied text.
		{http.MethodGet, "/v1/panel/notices"},
		{http.MethodPut, "/v1/panel/notices/expiry"},
		{http.MethodDelete, "/v1/panel/notices/expiry"},
		{http.MethodPost, "/v1/panel/notices/expiry/preview"},
		{http.MethodPost, "/v1/panel/notices/expiry/test"},
		// A test notification sends a real message to a real person. Reading the
		// history is gated with it: it is part of a customer record, and a
		// delivery log names every kind of notice somebody has received.
		{http.MethodGet, "/v1/panel/customers/00000000-0000-0000-0000-000000000000/notifications"},
		{http.MethodGet, "/v1/panel/customers/00000000-0000-0000-0000-000000000000/notifications/summary"},
		{http.MethodPost, "/v1/panel/customers/00000000-0000-0000-0000-000000000000/notifications/test"},
		// Pausing suspends a customer's access and stops their clock, so it is a
		// subscription mutation like any other and gated like one.
		{http.MethodPost, "/v1/panel/customers/00000000-0000-0000-0000-000000000000/subscriptions/00000000-0000-0000-0000-000000000000/pause"},
		{http.MethodPost, "/v1/panel/customers/00000000-0000-0000-0000-000000000000/subscriptions/00000000-0000-0000-0000-000000000000/resume"},
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
		"/v1/panel/settings/theme",
		// The public half is absent too. An installation with no panel has no
		// theme to publish, and a route that answered would be answering from
		// nothing.
		"/v1/branding",
		"/v1/pages",
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s answered %d without an operations service, want 404", path, recorder.Code)
		}
	}
}

// The public surfaces are public on purpose, and that is exactly why they need
// asserting rather than assuming.
//
// A sign-in screen has to render an installation's own colours before anybody
// has a session, and a payment provider's reviewer has to read the offer and
// the privacy policy without an account. So these routes sit outside
// `/v1/panel` and outside its gate. What must stay true is that being outside
// the gate buys nothing else: they are readable, and there is no method on them
// that writes. Writing happens on the panel routes above, behind the session
// and behind a permission.
func TestThePublicSurfacesAreReadOnly(t *testing.T) {
	router := mountedRouter(t)

	public := []string{"/v1/branding", "/v1/pages"}
	isPublic := func(pattern string) bool {
		for _, prefix := range public {
			if strings.HasPrefix(pattern, prefix) {
				return true
			}
		}
		return false
	}

	found := map[string]bool{}
	err := chi.Walk(router, func(
		method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler,
	) error {
		if !isPublic(pattern) {
			return nil
		}
		if mutatingMethods[method] {
			t.Errorf(
				"%s %s is a public route that mutates; these are served without a "+
					"session and must never be writable there",
				method, pattern,
			)
		}
		found[method+" "+pattern] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	for _, route := range []string{
		"GET /v1/branding",
		"GET /v1/branding/assets/{kind}",
		"GET /v1/pages",
		"GET /v1/pages/{slug}",
	} {
		if !found[route] {
			t.Errorf("%s is not mounted", route)
		}
	}
}
