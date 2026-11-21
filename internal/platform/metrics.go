package platform

import (
	"context"
	"errors"
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

	RemnawaveDuration *prometheus.HistogramVec
	PassRuns          *prometheus.CounterVec
	PassDuration      *prometheus.HistogramVec
	PassLastSuccess   *prometheus.GaugeVec
	QueueJobs         *prometheus.GaugeVec
	QueueOldestAge    *prometheus.GaugeVec
	BuildInfo         *prometheus.GaugeVec
	BackupLastSuccess prometheus.Gauge
}

// passBuckets spans a sub-second sweep to an hour-long backup.
var passBuckets = []float64{0.05, 0.25, 1, 5, 15, 60, 300, 900, 3600}

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
			"Outbound Bot API calls other than the long poll, by method and outcome.", "method", "outcome"),
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
		RemnawaveDuration: factory.histogramVec("omniflow_remnawave_request_duration_seconds",
			"Remnawave API call duration in seconds, by operation.", prometheus.DefBuckets, "operation"),
		PassRuns: factory.counterVec("omniflow_periodic_pass_runs_total",
			"Periodic worker passes run, by pass name and outcome.", "pass", "outcome"),
		PassDuration: factory.histogramVec("omniflow_periodic_pass_duration_seconds",
			"Periodic worker pass duration in seconds.", passBuckets, "pass"),
		PassLastSuccess: factory.gaugeVec("omniflow_periodic_pass_last_success_timestamp_seconds",
			"Unix time the pass last finished without an error.", "pass"),
		QueueJobs: factory.gaugeVec("omniflow_queue_jobs",
			"River jobs by queue and state, sampled by the worker.", "queue", "state"),
		QueueOldestAge: factory.gaugeVec("omniflow_queue_oldest_available_seconds",
			"Age in seconds of the oldest job waiting to run, by queue; 0 when none waits.", "queue"),
		BackupLastSuccess: factory.gauge("omniflow_backup_last_success_timestamp_seconds",
			"Unix time the newest verified scheduled backup completed; 0 when there is none."),
		BuildInfo: factory.gaugeVec("omniflow_build_info",
			"Always 1; carries the running build's version as a label.", "version"),
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

// ObservePass records one periodic pass run. A pass that ran out of its own
// timeout is recorded as "timeout", so a pass that never finishes is not
// mistaken for one that keeps failing on bad data.
func (metrics *Metrics) ObservePass(name string, err error, elapsed time.Duration) {
	if metrics == nil {
		return
	}
	outcome := "succeeded"
	switch {
	case err == nil:
		metrics.PassLastSuccess.WithLabelValues(name).SetToCurrentTime()
	case errors.Is(err, context.DeadlineExceeded):
		outcome = "timeout"
	default:
		outcome = "failed"
	}
	metrics.PassRuns.WithLabelValues(name, outcome).Inc()
	metrics.PassDuration.WithLabelValues(name).Observe(elapsed.Seconds())
}

// ObserveRemnawave records one Remnawave API call. outcome is "succeeded",
// "not_found" (an ordinary lookup miss), or "failed"; code classifies a
// failure and is counted only for one.
func (metrics *Metrics) ObserveRemnawave(operation, outcome, code string, elapsed time.Duration) {
	if metrics == nil {
		return
	}
	if outcome == "failed" {
		metrics.RemnawaveErrors.WithLabelValues(code).Inc()
	}
	metrics.RemnawaveCalls.WithLabelValues(operation, outcome).Inc()
	metrics.RemnawaveDuration.WithLabelValues(operation).Observe(elapsed.Seconds())
}

// ObserveTelegramSend records one outbound Bot API call. outcome is a
// bounded classification: ok, blocked, rate_limited, rejected, not_found,
// telegram_error, or unavailable.
func (metrics *Metrics) ObserveTelegramSend(method, outcome string) {
	if metrics == nil {
		return
	}
	metrics.TelegramSends.WithLabelValues(method, outcome).Inc()
}

// ObserveTelegramUpdate records one inbound update and whether its handler
// succeeded.
func (metrics *Metrics) ObserveTelegramUpdate(kind, outcome string) {
	if metrics == nil {
		return
	}
	metrics.TelegramUpdates.WithLabelValues(kind, outcome).Inc()
}

// SetOutboxPending records the number of undispatched outbox events.
func (metrics *Metrics) SetOutboxPending(count int64) {
	if metrics == nil {
		return
	}
	metrics.OutboxPending.Set(float64(count))
}

// QueueSample is one queue's state counts and the age of its oldest waiting
// job.
type QueueSample struct {
	Queue            string
	States           map[string]int64
	OldestAvailableS float64
}

// SetQueues replaces the queue gauges with the latest sample. States missing
// from a sample are reset to zero rather than left at a stale value.
func (metrics *Metrics) SetQueues(samples []QueueSample) {
	if metrics == nil {
		return
	}
	metrics.QueueJobs.Reset()
	metrics.QueueOldestAge.Reset()
	for _, sample := range samples {
		for state, count := range sample.States {
			metrics.QueueJobs.WithLabelValues(sample.Queue, state).Set(float64(count))
		}
		metrics.QueueOldestAge.WithLabelValues(sample.Queue).Set(sample.OldestAvailableS)
	}
}

// SetBackupLastSuccess records when the newest verified backup completed.
func (metrics *Metrics) SetBackupLastSuccess(at time.Time) {
	if metrics == nil {
		return
	}
	if at.IsZero() {
		metrics.BackupLastSuccess.Set(0)
		return
	}
	metrics.BackupLastSuccess.Set(float64(at.Unix()))
}

// SetBuild records the running version.
func (metrics *Metrics) SetBuild(version string) {
	if metrics == nil {
		return
	}
	if version == "" {
		version = "dev"
	}
	metrics.BuildInfo.Reset()
	metrics.BuildInfo.WithLabelValues(version).Set(1)
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
