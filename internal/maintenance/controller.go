// Package maintenance keeps the installation-wide maintenance switch in step
// with the health of the systems Omniflow depends on.
//
// Maintenance blocks new purchases and holds outbound provisioning. It never
// touches money that has already been taken: settlement, refunds, and order
// history keep working throughout.
package maintenance

import (
	"context"
	"log/slog"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/platform"
)

// Store is the persistence the controller needs. commercepg.Store satisfies it.
type Store interface {
	Maintenance(ctx context.Context) (commerce.Maintenance, error)
	SetMaintenance(ctx context.Context, desired commerce.Maintenance, actorType, actorID string) (commerce.Maintenance, error)
}

// Announcer is notified whenever maintenance is entered or left, so an operator
// group and the customer-facing notice stay in step with the switch.
type Announcer interface {
	MaintenanceChanged(ctx context.Context, state commerce.Maintenance)
}

// Controller samples dependency health and moves the switch when a documented
// streak is reached.
type Controller struct {
	store     Store
	health    *platform.Health
	metrics   *platform.Metrics
	logger    *slog.Logger
	announcer Announcer
	config    Config

	failures   int
	recoveries int
}

// Config is the detection policy.
type Config struct {
	AutoDetect     bool
	ProbeInterval  time.Duration
	FailureStreak  int
	RecoveryStreak int
	// Watch names the dependencies whose health drives automatic maintenance.
	// A probe that is registered but not watched still appears in /readyz.
	Watch []string
}

func NewController(store Store, health *platform.Health, metrics *platform.Metrics, logger *slog.Logger, config Config) *Controller {
	if config.ProbeInterval <= 0 {
		config.ProbeInterval = 30 * time.Second
	}
	if len(config.Watch) == 0 {
		config.Watch = []string{"postgres", "remnawave"}
	}
	return &Controller{store: store, health: health, metrics: metrics, logger: logger, config: config}
}

// WithAnnouncer attaches the notifier that tells operators about a transition.
func (controller *Controller) WithAnnouncer(announcer Announcer) *Controller {
	controller.announcer = announcer
	return controller
}

// Run samples health until the context is cancelled.
func (controller *Controller) Run(ctx context.Context) {
	ticker := time.NewTicker(controller.config.ProbeInterval)
	defer ticker.Stop()
	for {
		controller.Sample(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Sample takes one health reading and applies the resulting decision. It is
// exported so a test can drive the controller one step at a time.
func (controller *Controller) Sample(ctx context.Context) {
	checks, _ := controller.health.Report(ctx)
	health := commerce.DependencyHealth{RemnawaveHealthy: true, DatabaseHealthy: true, ValkeyHealthy: true}
	watched := make(map[string]struct{}, len(controller.config.Watch))
	for _, name := range controller.config.Watch {
		watched[name] = struct{}{}
	}
	for _, check := range checks {
		controller.metrics.SetDependency(check.Name, check.Healthy)
		if _, ok := watched[check.Name]; !ok || check.Healthy {
			continue
		}
		switch check.Name {
		case "postgres":
			health.DatabaseHealthy = false
		case "remnawave":
			health.RemnawaveHealthy = false
		case "valkey":
			health.ValkeyHealthy = false
		}
	}
	if health.Healthy() {
		controller.recoveries++
		controller.failures = 0
	} else {
		controller.failures++
		controller.recoveries = 0
	}
	current, err := controller.store.Maintenance(ctx)
	if err != nil {
		controller.logger.Warn("maintenance state lookup failed", "error", err)
		return
	}
	controller.metrics.SetMaintenance(current.Active)
	if !controller.config.AutoDetect {
		return
	}
	decision := commerce.EvaluateMaintenance(current, health, controller.failures, controller.recoveries, controller.config.FailureStreak, controller.config.RecoveryStreak)
	if !decision.Activate && !decision.Clear {
		return
	}
	desired := current
	desired.Active, desired.Source, desired.Reason = decision.Activate, decision.Source, decision.Reason
	updated, err := controller.store.SetMaintenance(ctx, desired, "system", "maintenance-controller")
	if err != nil {
		controller.logger.Error("maintenance transition failed", "error", err, "activate", decision.Activate)
		return
	}
	controller.metrics.SetMaintenance(updated.Active)
	controller.logger.Warn("maintenance mode changed", "active", updated.Active, "source", updated.Source, "reason", updated.Reason)
	if controller.announcer != nil {
		controller.announcer.MaintenanceChanged(ctx, updated)
	}
}
