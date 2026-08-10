package telemetry

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCollectorRejectsUndocumentedPayload(t *testing.T) {
	t.Parallel()
	client := NewClient(slog.New(slog.NewTextHandler(io.Discard, nil)), Config{InstallationID: "00000000-0000-4000-8000-000000000000"})
	request := httptest.NewRequest(http.MethodPost, "/v1/telemetry/events", strings.NewReader(`{"schema":1}`))
	response := httptest.NewRecorder()

	client.CollectorHandler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
}
