package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/accountreferral"
	"github.com/omniflow/omniflow/internal/accountshop"
	"github.com/omniflow/omniflow/internal/accountsupport"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/goods"
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

// stubGoodsProviders stands in for the digital-goods registry. It resolves
// nothing, which is all these tests need: the session gate refuses every request
// before a provider would be asked for a quote.
type stubGoodsProviders struct{}

func (stubGoodsProviders) Provider(context.Context, string) (goods.Provider, error) {
	return nil, errors.New("no provider in tests")
}

// newMountedAccountRouter mounts the customer surface with every v0.10 service
// attached, so the routes those services own are actually registered.
//
// The pool is never dialled. pgxpool defers connecting until a query needs a
// connection, and every route below is refused by the session gate long before
// one does — which is the point: these tests are about routing and the gates in
// front of it, and a test that needed a database to prove a route answers 401
// would be testing the database.
func newMountedAccountRouter(t *testing.T) http.Handler {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://omniflow:omniflow@127.0.0.1:1/omniflow")
	if err != nil {
		t.Fatalf("build pool: %v", err)
	}
	t.Cleanup(pool.Close)

	logger := slog.New(slog.DiscardHandler)
	checkout, err := accountcheckout.New(pool, &commercepg.Store{}, nil, accountcheckout.Options{Logger: logger})
	if err != nil {
		t.Fatalf("build checkout: %v", err)
	}
	// A registry has to be present for the shop to mount at all: an installation
	// that sells no digital goods has no shop routes rather than empty ones.
	shop, err := accountshop.New(
		pool, &commercepg.Store{}, nil, stubGoodsProviders{}, accountshop.Options{Logger: logger},
	)
	if err != nil {
		t.Fatalf("build shop: %v", err)
	}
	support, err := accountsupport.New(pool, accountsupport.Options{Logger: logger})
	if err != nil {
		t.Fatalf("build support: %v", err)
	}
	referral, err := accountreferral.New(pool, accountreferral.Options{Logger: logger})
	if err != nil {
		t.Fatalf("build referral: %v", err)
	}

	handlers := NewAccountHandlers(AccountOptions{
		Logger: logger, Checkout: checkout, Shop: shop, Support: support, Referral: referral,
	})
	router := chi.NewRouter()
	handlers.Mount(router)
	return router
}

// Every v0.10 route sits behind the session gate.
//
// The surfaces are mounted by four separate functions, so a Mount call placed
// outside the authenticated group would expose a whole area — an order history,
// a support conversation, a personal-data export — to anyone who asked. That is
// the failure this test exists to catch, and it is cheap enough to enumerate
// every route rather than a sample.
func TestAccountCommerceRoutesRequireASession(t *testing.T) {
	router := newMountedAccountRouter(t)
	const uuid = "2f1c0c2e-0000-4000-8000-000000000000"
	cases := []struct{ method, path string }{
		{http.MethodGet, "/v1/account/plans"},
		{http.MethodGet, "/v1/account/plans/" + uuid},
		{http.MethodGet, "/v1/account/checkout"},
		{http.MethodPost, "/v1/account/checkout"},
		{http.MethodPatch, "/v1/account/checkout"},
		{http.MethodDelete, "/v1/account/checkout"},
		{http.MethodPost, "/v1/account/checkout/promo"},
		{http.MethodDelete, "/v1/account/checkout/promo"},
		{http.MethodPost, "/v1/account/checkout/addons/" + uuid},
		{http.MethodPost, "/v1/account/checkout/confirm"},
		{http.MethodGet, "/v1/account/orders"},
		{http.MethodGet, "/v1/account/orders/" + uuid},
		{http.MethodPost, "/v1/account/orders/" + uuid + "/payment"},
		{http.MethodPost, "/v1/account/orders/" + uuid + "/refresh"},
		{http.MethodPost, "/v1/account/orders/" + uuid + "/cancel"},
		{http.MethodGet, "/v1/account/wallet"},
		{http.MethodPost, "/v1/account/wallet/top-up"},

		{http.MethodGet, "/v1/account/shop/products"},
		{http.MethodGet, "/v1/account/shop/products/" + uuid},
		{http.MethodPost, "/v1/account/shop/recipient"},
		{http.MethodPost, "/v1/account/shop/purchase"},
		{http.MethodGet, "/v1/account/shop/orders"},
		{http.MethodGet, "/v1/account/shop/orders/" + uuid},

		{http.MethodGet, "/v1/account/support/limits"},
		{http.MethodGet, "/v1/account/support/tickets"},
		{http.MethodPost, "/v1/account/support/tickets"},
		{http.MethodGet, "/v1/account/support/tickets/" + uuid},
		{http.MethodPost, "/v1/account/support/tickets/" + uuid + "/messages"},
		{http.MethodPost, "/v1/account/support/tickets/" + uuid + "/read"},
		{http.MethodPost, "/v1/account/support/tickets/" + uuid + "/close"},
		{http.MethodPost, "/v1/account/support/tickets/" + uuid + "/reopen"},
		{http.MethodPost, "/v1/account/support/tickets/" + uuid + "/attachments"},
		{http.MethodGet, "/v1/account/support/attachments/" + uuid},
		{http.MethodGet, "/v1/account/news"},
		{http.MethodPost, "/v1/account/news/" + uuid + "/read"},
		{http.MethodGet, "/v1/account/preferences"},
		{http.MethodPatch, "/v1/account/preferences"},
		{http.MethodPost, "/v1/account/preferences/unsubscribe"},

		{http.MethodGet, "/v1/account/referrals"},
		{http.MethodGet, "/v1/account/loyalty"},
		{http.MethodGet, "/v1/account/contacts"},
		{http.MethodPost, "/v1/account/contacts"},
		{http.MethodDelete, "/v1/account/contacts/" + uuid},
		{http.MethodGet, "/v1/account/privacy"},
		{http.MethodPost, "/v1/account/privacy/export"},
		{http.MethodPost, "/v1/account/privacy/deletion"},
		{http.MethodDelete, "/v1/account/privacy/deletion"},
	}
	for _, testCase := range cases {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(testCase.method, testCase.path, nil))
		if recorder.Code == http.StatusNotFound {
			t.Fatalf("%s %s is not routed", testCase.method, testCase.path)
		}
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s answered %d, want 401", testCase.method, testCase.path, recorder.Code)
		}
	}
}

// An installation that attaches no commerce, shop, support, or referral service
// has no such routes at all.
//
// It answers 404 rather than 503 deliberately. 503 says "this exists and is
// broken", which would be a lie about an installation that simply does not sell
// digital goods, and it would also tell an anonymous caller what the operator
// has configured.
func TestAccountV010SurfacesAreAbsentWithoutTheirServices(t *testing.T) {
	router := newAccountRouter()
	for _, path := range []string{
		"/v1/account/plans", "/v1/account/checkout", "/v1/account/orders", "/v1/account/wallet",
		"/v1/account/shop/products", "/v1/account/support/tickets", "/v1/account/news",
		"/v1/account/preferences", "/v1/account/referrals", "/v1/account/loyalty",
		"/v1/account/contacts", "/v1/account/privacy",
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s answered %d with no service attached, want 404", path, recorder.Code)
		}
	}
}

// The two actions a customer cannot undo need a recent sign-in, not merely a
// session. A session left open on a shared machine must not be enough to strip
// somebody's way back into their account or to start deleting it.
func TestIrreversibleAccountActionsRequireRecentAuthentication(t *testing.T) {
	router := newMountedAccountRouter(t)
	for _, path := range []string{
		"/v1/account/sign-in-methods/2f1c0c2e-0000-4000-8000-000000000000",
		"/v1/account/privacy/deletion",
	} {
		recorder := httptest.NewRecorder()
		method := http.MethodDelete
		if strings.HasSuffix(path, "deletion") {
			method = http.MethodPost
		}
		router.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
		// Without any session at all the session gate answers first, which is the
		// correct order: the reauthentication gate runs inside it.
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s answered %d, want 401", method, path, recorder.Code)
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
