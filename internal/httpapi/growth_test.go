package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/emailpg"
	"github.com/omniflow/omniflow/internal/referralpayout"
)

var growthTestKey = []byte("0123456789abcdef0123456789abcdef")

// growthServicesForRouting builds the services with no database. Every route
// test below stops at a gate or at token verification, before a query.
func growthServicesForRouting(t *testing.T) GrowthServices {
	t.Helper()
	email, err := emailpg.New(nil, emailpg.Options{DataKey: growthTestKey})
	if err != nil {
		t.Fatal(err)
	}
	withdrawals, err := referralpayout.New(nil, growthTestKey, email, nil)
	if err != nil {
		t.Fatal(err)
	}
	return GrowthServices{Email: email, Withdrawals: withdrawals}
}

func TestGrowthPanelRoutesRequireASession(t *testing.T) {
	handlers := NewAdminHandlers(AdminOptions{Logger: slog.New(slog.DiscardHandler)}).
		WithGrowth(growthServicesForRouting(t))
	router := chi.NewRouter()
	handlers.Mount(router)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/v1/panel/settings/email"},
		{http.MethodPost, "/v1/panel/settings/email/test"},
		{http.MethodGet, "/v1/panel/finance/withdrawals"},
		{http.MethodPut, "/v1/panel/finance/withdrawals/settings"},
		{http.MethodPost, "/v1/panel/finance/withdrawals/x/approve"},
		{http.MethodPost, "/v1/panel/finance/withdrawals/x/details"},
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(route.method, route.path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s %s answered %d, want 401", route.method, route.path, recorder.Code)
		}
	}
}

func TestGrowthAccountRoutes(t *testing.T) {
	handlers := NewAccountHandlers(AccountOptions{Logger: slog.New(slog.DiscardHandler)}).
		WithGrowth(growthServicesForRouting(t))
	router := chi.NewRouter()
	handlers.Mount(router)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/v1/account/referrals/withdrawals"},
		{http.MethodPost, "/v1/account/referrals/withdrawals"},
		{http.MethodDelete, "/v1/account/referrals/withdrawals/x"},
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(route.method, route.path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s %s answered %d, want 401", route.method, route.path, recorder.Code)
		}
	}

	// The one-click unsubscribe is reachable with no session and no origin, as
	// RFC 8058 requires, and a forged token is refused before anything is read.
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/account/email/unsubscribe?token=forged.token",
		strings.NewReader("List-Unsubscribe=One-Click"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound || problemCode(t, recorder) != "unsubscribe_invalid" {
		t.Fatalf("forged unsubscribe answered %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/account/email/unsubscribe?token=a.b", nil))
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/unsubscribe?token=a.b" {
		t.Fatalf("footer link answered %d to %q", recorder.Code, recorder.Header().Get("Location"))
	}
}
