package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/omniflow/omniflow/internal/platform"
)

// The API listener is the public one, and /metrics on it describes
// installation internals. With APP_METRICS_TOKEN set it answers only to that
// bearer token; the probes stay open for the orchestrator.
func TestMetricsRequireTheConfiguredBearerToken(t *testing.T) {
	router := NewRouter(slog.New(slog.DiscardHandler), RouterOptions{
		Metrics: platform.NewMetrics("api-test"), MetricsToken: "scrape-secret",
	})
	get := func(path, authorization string) int {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder.Code
	}
	if code := get("/metrics", ""); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated scrape = %d, want 401", code)
	}
	if code := get("/metrics", "Bearer wrong"); code != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d, want 401", code)
	}
	if code := get("/metrics", "Bearer scrape-secret"); code != http.StatusOK {
		t.Fatalf("correct token = %d, want 200", code)
	}
	if code := get("/livez", ""); code != http.StatusOK {
		t.Fatalf("liveness probe = %d, want 200", code)
	}
}
