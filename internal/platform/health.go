package platform

import (
	"context"
	"sync"
	"time"
)

// Probe reports whether one dependency is answering. A probe must be cheap and
// must never block longer than the context allows.
type Probe func(context.Context) error

// Check is one named dependency in a health report.
type Check struct {
	Name    string `json:"name"`
	Healthy bool   `json:"healthy"`
	// Error is a short classification, never the underlying message, so a
	// health endpoint cannot leak a connection string or a credential.
	Error       string    `json:"error,omitempty"`
	CheckedAt   time.Time `json:"checkedAt"`
	LatencyMS   int64     `json:"latencyMs"`
	Consecutive int       `json:"consecutiveFailures,omitempty"`
}

// Health aggregates dependency probes behind a short cache, so a burst of
// readiness requests cannot turn into a burst of database round trips.
type Health struct {
	mutex   sync.Mutex
	probes  map[string]Probe
	order   []string
	results map[string]Check
	streak  map[string]int
	ttl     time.Duration
	timeout time.Duration
	clock   func() time.Time
}

// NewHealth builds a health registry. ttl bounds how long a result is reused and
// timeout bounds one probe.
func NewHealth(ttl, timeout time.Duration) *Health {
	if ttl <= 0 {
		ttl = time.Second
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &Health{
		probes: map[string]Probe{}, results: map[string]Check{}, streak: map[string]int{},
		ttl: ttl, timeout: timeout, clock: time.Now,
	}
}

// Register adds a dependency probe. Registration order is the report order.
func (health *Health) Register(name string, probe Probe) {
	health.mutex.Lock()
	defer health.mutex.Unlock()
	if _, exists := health.probes[name]; !exists {
		health.order = append(health.order, name)
	}
	health.probes[name] = probe
}

// Report runs every probe that has no fresh cached result and returns the full
// picture together with whether every dependency is healthy.
func (health *Health) Report(ctx context.Context) ([]Check, bool) {
	health.mutex.Lock()
	names := append([]string(nil), health.order...)
	probes := make(map[string]Probe, len(names))
	cached := make(map[string]Check, len(names))
	for _, name := range names {
		probes[name] = health.probes[name]
		cached[name] = health.results[name]
	}
	ttl, timeout, now := health.ttl, health.timeout, health.clock()
	health.mutex.Unlock()

	checks := make([]Check, 0, len(names))
	healthy := true
	for _, name := range names {
		result, fresh := cached[name], false
		if !result.CheckedAt.IsZero() && now.Sub(result.CheckedAt) < ttl {
			fresh = true
		}
		if !fresh {
			result = runProbe(ctx, name, probes[name], timeout, now)
			health.record(name, &result)
		}
		if !result.Healthy {
			healthy = false
		}
		checks = append(checks, result)
	}
	return checks, healthy
}

func (health *Health) record(name string, result *Check) {
	health.mutex.Lock()
	defer health.mutex.Unlock()
	if result.Healthy {
		health.streak[name] = 0
	} else {
		health.streak[name]++
	}
	result.Consecutive = health.streak[name]
	health.results[name] = *result
}

func runProbe(ctx context.Context, name string, probe Probe, timeout time.Duration, now time.Time) Check {
	check := Check{Name: name, CheckedAt: now, Healthy: true}
	if probe == nil {
		return check
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	if err := probe(probeCtx); err != nil {
		check.Healthy = false
		check.Error = classifyProbeError(err, probeCtx)
	}
	check.LatencyMS = time.Since(started).Milliseconds()
	return check
}

// classifyProbeError reduces a dependency failure to a stable code. The
// underlying message is deliberately dropped: it routinely contains a DSN, a
// host name, or a credential.
func classifyProbeError(err error, ctx context.Context) string {
	if ctx.Err() != nil {
		return "timeout"
	}
	if err == nil {
		return ""
	}
	return "unavailable"
}
