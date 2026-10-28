package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/panelsearchpg"
)

// Record search reads customer data, so it sits behind the session gate like
// every list screen it stands in for, and is absent when no adapter is wired.
func TestRecordSearchRequiresASession(t *testing.T) {
	handlers := NewAdminHandlers(AdminOptions{
		Logger: slog.New(slog.DiscardHandler), Search: &panelsearchpg.Service{},
	})
	router := chi.NewRouter()
	handlers.Mount(router)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/panel/search?q=ivan", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("GET /v1/panel/search answered %d without a session, want 401", recorder.Code)
	}
}
