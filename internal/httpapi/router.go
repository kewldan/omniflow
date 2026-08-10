package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/omniflow/omniflow/internal/telemetry"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func NewRouter(logger *slog.Logger, version string, telemetryClient *telemetry.Client, collectorEnabled bool, commerceHandlers *CommerceHandlers) http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("X-Request-ID", middleware.GetReqID(request.Context()))
			next.ServeHTTP(writer, request)
		})
	})
	router.Use(middleware.Recoverer)
	router.Use(middleware.Timeout(60 * time.Second))
	router.Use(func(next http.Handler) http.Handler { return otelhttp.NewHandler(next, "http.server") })

	router.Get("/healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ok", "version": version})
	})
	router.Get("/readyz", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
	})
	if collectorEnabled {
		router.Post("/v1/telemetry/events", telemetryClient.CollectorHandler())
	}
	if commerceHandlers != nil {
		commerceHandlers.Mount(router)
	}

	_ = logger
	return router
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	if writer.Header().Get("Content-Type") == "" {
		writer.Header().Set("Content-Type", "application/json")
	}
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
