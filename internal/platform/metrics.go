package platform

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics is the Prometheus surface shared by the API, the bot, and the worker.
// Every label is a bounded, non-identifying value: a route template, a provider
// name, an operation, or an outcome. No customer, order, or payment identifier
// ever becomes a label, because label values are retained indefinitely by a
// scrape target.
type Metrics struct {
	registry *prometheus.Registry

	HTTPRequests    *prometheus.CounterVec
	HTTPDuration    *prometheus.HistogramVec
	TelegramUpdates *prometheus.CounterVec
	TelegramSends   *prometheus.CounterVec
	Jobs            *prometheus.CounterVec
	JobDuration     *prometheus.HistogramVec
	Webhooks        *prometheus.CounterVec
	Payments        *prometheus.CounterVec
	RemnawaveCalls  *prometheus.CounterVec
	RemnawaveErrors *prometheus.CounterVec
	OutboxPending   prometheus.Gauge
	MaintenanceMode prometheus.Gauge
	DependencyUp    *prometheus.GaugeVec
}

// NewMetrics builds an isolated registry so two processes in one test binary do
// not fight over the default registerer.
func NewMetrics(service string) *Metrics {
	labels := prometheus.Labels{"service": service}
	registry := prometheus.NewRegistry()
	factory := promauto(registry, labels)
	metrics := &Metrics{
		registry: registry,
		HTTPRequests: factory.counterVec("omniflow_http_requests_total",
			"HTTP requests handled, by route template, method, and status class.", "route", "method", "status"),
		HTTPDuration: factory.histogramVec("omniflow_http_request_duration_seconds",
			"HTTP request duration in seconds.", prometheus.DefBuckets, "route", "method"),
		TelegramUpdates: factory.counterVec("omniflow_telegram_updates_total",
			"Telegram updates processed, by kind and outcome.", "kind", "outcome"),
		TelegramSends: factory.counterVec("omniflow_telegram_sends_total",
			"Telegram delivery attempts, by outcome classification.", "outcome"),
		Jobs: factory.counterVec("omniflow_jobs_total",
			"Durable jobs completed, by job kind and outcome.", "kind", "outcome"),
		JobDuration: factory.histogramVec("omniflow_job_duration_seconds",
			"Durable job duration in seconds.", prometheus.DefBuckets, "kind"),
		Webhooks: factory.counterVec("omniflow_provider_webhooks_total",
			"Provider webhooks received, by provider and outcome.", "provider", "outcome"),
		Payments: factory.counterVec("omniflow_payments_total",
			"Payment settlements, by provider, operation, and classification.", "provider", "operation", "classification"),
		RemnawaveCalls: factory.counterVec("omniflow_remnawave_requests_total",
			"Remnawave API calls, by operation and outcome.", "operation", "outcome"),
		RemnawaveErrors: factory.counterVec("omniflow_remnawave_errors_total",
			"Remnawave API failures, by classified error code.", "code"),
		OutboxPending: factory.gauge("omniflow_outbox_pending_events",
			"Domain events published to the outbox but not yet dispatched."),
		MaintenanceMode: factory.gauge("omniflow_maintenance_mode",
			"1 while the installation is in maintenance mode, 0 otherwise."),
		DependencyUp: factory.gaugeVec("omniflow_dependency_up",
			"1 while a dependency answers its health probe, 0 otherwise.", "dependency"),
	}
	registry.MustRegister(collectors.NewGoCollector())
	registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return metrics
}

// Handler serves the registry in the Prometheus text exposition format.
func (metrics *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(metrics.registry, promhttp.HandlerOpts{})
}

// Registry exposes the underlying registry for tests and for embedding.
func (metrics *Metrics) Registry() *prometheus.Registry { return metrics.registry }

// ObserveHTTP records one served request. The route must be a template such as
// "/v1/admin/orders/{orderID}", never a concrete path with identifiers in it.
func (metrics *Metrics) ObserveHTTP(route, method string, status int, elapsed time.Duration) {
	if metrics == nil {
		return
	}
	metrics.HTTPRequests.WithLabelValues(route, method, statusClass(status)).Inc()
	metrics.HTTPDuration.WithLabelValues(route, method).Observe(elapsed.Seconds())
}

// ObserveJob records one durable job run.
func (metrics *Metrics) ObserveJob(kind, outcome string, elapsed time.Duration) {
	if metrics == nil {
		return
	}
	metrics.Jobs.WithLabelValues(kind, outcome).Inc()
	metrics.JobDuration.WithLabelValues(kind).Observe(elapsed.Seconds())
}

// SetDependency records the latest health of one dependency.
func (metrics *Metrics) SetDependency(name string, healthy bool) {
	if metrics == nil {
		return
	}
	value := 0.0
	if healthy {
		value = 1
	}
	metrics.DependencyUp.WithLabelValues(name).Set(value)
}

// SetMaintenance records whether the installation is in maintenance mode.
func (metrics *Metrics) SetMaintenance(active bool) {
	if metrics == nil {
		return
	}
	value := 0.0
	if active {
		value = 1
	}
	metrics.MaintenanceMode.Set(value)
}

// statusClass buckets a status code so the label stays low-cardinality.
func statusClass(status int) string {
	if status < 100 || status > 599 {
		return "unknown"
	}
	return strconv.Itoa(status/100) + "xx"
}

type metricFactory struct {
	registry *prometheus.Registry
	labels   prometheus.Labels
}

func promauto(registry *prometheus.Registry, labels prometheus.Labels) metricFactory {
	return metricFactory{registry: registry, labels: labels}
}

func (factory metricFactory) counterVec(name, help string, labelNames ...string) *prometheus.CounterVec {
	metric := prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help, ConstLabels: factory.labels}, labelNames)
	factory.registry.MustRegister(metric)
	return metric
}

func (factory metricFactory) histogramVec(name, help string, buckets []float64, labelNames ...string) *prometheus.HistogramVec {
	metric := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: name, Help: help, Buckets: buckets, ConstLabels: factory.labels}, labelNames)
	factory.registry.MustRegister(metric)
	return metric
}

func (factory metricFactory) gauge(name, help string) prometheus.Gauge {
	metric := prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help, ConstLabels: factory.labels})
	factory.registry.MustRegister(metric)
	return metric
}

func (factory metricFactory) gaugeVec(name, help string, labelNames ...string) *prometheus.GaugeVec {
	metric := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name, Help: help, ConstLabels: factory.labels}, labelNames)
	factory.registry.MustRegister(metric)
	return metric
}
