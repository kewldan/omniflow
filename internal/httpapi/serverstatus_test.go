package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/remnawave"
)

type countingNodes struct{ calls int }

func (lister *countingNodes) Nodes(context.Context) ([]remnawave.Node, error) {
	lister.calls++
	return []remnawave.Node{
		{Name: "Riga", CountryCode: "LV", IsConnected: true, TrafficUsedBytes: 99},
		{Name: "Amsterdam", CountryCode: "NL", IsConnected: false},
		{Name: "Spare", IsDisabled: true},
	}, nil
}

func TestServerStatusIsOptInMinimalAndCached(t *testing.T) {
	lister := &countingNodes{}
	if NewServerStatusHandler(lister, slog.New(slog.DiscardHandler), func(string) string { return "" }) != nil {
		t.Fatal("the status page must be off by default")
	}
	handler := NewServerStatusHandler(lister, slog.New(slog.DiscardHandler), func(key string) string {
		return map[string]string{"APP_SERVER_STATUS_ENABLED": "true"}[key]
	})
	router := chi.NewRouter()
	handler.Mount(router)
	for range 2 {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/status/servers", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
		body := recorder.Body.String()
		if strings.Contains(body, "Spare") || strings.Contains(body, "99") {
			t.Fatalf("a disabled node or a traffic figure leaked: %s", body)
		}
		var payload struct {
			Servers []statusNode `json:"servers"`
		}
		_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
		if len(payload.Servers) != 2 || payload.Servers[0].Name != "Amsterdam" || payload.Servers[1].Online != true {
			t.Fatalf("servers = %+v", payload.Servers)
		}
	}
	if lister.calls != 1 {
		t.Fatalf("the panel was read %d times; the answer is cached", lister.calls)
	}
}
