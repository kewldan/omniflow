package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/omniflow/omniflow/internal/platform"
	"github.com/omniflow/omniflow/internal/telemetry"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// RouterOptions carries the optional runtime surfaces. A nil value simply
// leaves the corresponding endpoint out, so a minimal installation still boots.
type RouterOptions struct {
	Health   *platform.Health
	Metrics  *platform.Metrics
	Commerce *CommerceHandlers
	// Admin serves the operator panel API. A nil value leaves /v1/panel
	// unmounted, which is what a bot-only installation wants.
	Admin *AdminHandlers
	// CollectorEnabled exposes the anonymous telemetry collector endpoint.
	CollectorEnabled bool
	Telemetry        *telemetry.Client
	Version          string
}

func NewRouter(logger *slog.Logger, options RouterOptions) http.Handler {
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
	if options.Metrics != nil {
		router.Use(metricsMiddleware(options.Metrics))
	}

	// /healthz stays for compatibility with existing deployments; /livez and
	// /readyz are the documented probes from v0.5 onward.
	liveness := func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ok", "version": options.Version})
	}
	router.Get("/healthz", liveness)
	router.Get("/livez", liveness)
	router.Get("/readyz", readinessHandler(logger, options.Health, options.Metrics))
	if options.Metrics != nil {
		router.Handle("/metrics", options.Metrics.Handler())
	}
	if options.CollectorEnabled && options.Telemetry != nil {
		router.Post("/v1/telemetry/events", options.Telemetry.CollectorHandler())
	}
	if options.Commerce != nil {
		options.Commerce.Mount(router)
	}
	if options.Admin != nil {
		options.Admin.Mount(router)
	}
	return router
}

// readinessHandler answers 200 only while every registered dependency responds.
// The body names each dependency and a stable failure classification; it never
// carries a connection string, a host name, or an underlying driver message.
func readinessHandler(logger *slog.Logger, health *platform.Health, metrics *platform.Metrics) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if health == nil {
			writeJSON(writer, http.StatusOK, map[string]any{"status": "ready", "checks": []platform.Check{}})
			return
		}
		checks, healthy := health.Report(request.Context())
		for _, check := range checks {
			metrics.SetDependency(check.Name, check.Healthy)
		}
		status, label := http.StatusOK, "ready"
		if !healthy {
			status, label = http.StatusServiceUnavailable, "degraded"
			logger.Warn("readiness probe failed", "checks", len(checks))
		}
		writeJSON(writer, status, map[string]any{"status": label, "checks": checks})
	}
}

// metricsMiddleware records request counts and latency against the chi route
// pattern, so a path with identifiers in it never becomes a metric label.
func metricsMiddleware(metrics *platform.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			started := time.Now()
			recorder := &statusRecorder{ResponseWriter: writer, status: http.StatusOK}
			next.ServeHTTP(recorder, request)
			route := "unmatched"
			if pattern := chi.RouteContext(request.Context()).RoutePattern(); pattern != "" {
				route = pattern
			}
			metrics.ObserveHTTP(route, request.Method, recorder.status, time.Since(started))
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (recorder *statusRecorder) WriteHeader(status int) {
	if !recorder.written {
		recorder.status, recorder.written = status, true
	}
	recorder.ResponseWriter.WriteHeader(status)
}

func (recorder *statusRecorder) Write(payload []byte) (int, error) {
	recorder.written = true
	return recorder.ResponseWriter.Write(payload)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	if writer.Header().Get("Content-Type") == "" {
		writer.Header().Set("Content-Type", "application/json")
	}
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
