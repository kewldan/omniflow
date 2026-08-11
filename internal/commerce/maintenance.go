package commerce

import "time"

// MaintenanceSource names what put the installation into maintenance. "manual"
// is an operator decision; the others are automatic dependency detection.
const (
	MaintenanceManual    = "manual"
	MaintenanceRemnawave = "remnawave"
	MaintenanceDatabase  = "database"
	MaintenanceValkey    = "valkey"
)

// Maintenance is the installation-wide maintenance record.
type Maintenance struct {
	Active bool
	Source string
	Reason string
	// NoticeRU and NoticeEN are the operator's own customer-facing wording. An
	// empty notice falls back to the built-in localized copy.
	NoticeRU         string
	NoticeEN         string
	ExpectedReturnAt time.Time
	ActivatedAt      time.Time
}

// BlockedAction is an operation maintenance mode refuses.
type BlockedAction string

const (
	// ActionPurchase covers creating an order or starting a payment. Blocking it
	// is what stops new money entering an installation that cannot provision.
	ActionPurchase BlockedAction = "purchase"
	// ActionFulfillment covers pushing entitlement state to Remnawave.
	ActionFulfillment BlockedAction = "fulfillment"
	// ActionBrowse covers read-only screens, which stay available so a customer
	// can still see the notice, their orders, and support.
	ActionBrowse BlockedAction = "browse"
)

// Blocks reports whether maintenance refuses an action. Money that has already
// been taken is never touched: settlement, refunds, and order history keep
// working, and only new purchases and outbound provisioning are held back.
func (maintenance Maintenance) Blocks(action BlockedAction) bool {
	if !maintenance.Active {
		return false
	}
	switch action {
	case ActionPurchase, ActionFulfillment:
		return true
	default:
		return false
	}
}

// DependencyHealth is one health sample of the systems maintenance mode watches.
type DependencyHealth struct {
	RemnawaveHealthy bool
	DatabaseHealthy  bool
	ValkeyHealthy    bool
}

// Healthy reports whether every watched dependency answered.
func (health DependencyHealth) Healthy() bool {
	return health.RemnawaveHealthy && health.DatabaseHealthy && health.ValkeyHealthy
}

// firstUnhealthy names the dependency that should be recorded as the source.
func (health DependencyHealth) firstUnhealthy() string {
	switch {
	case !health.DatabaseHealthy:
		return MaintenanceDatabase
	case !health.RemnawaveHealthy:
		return MaintenanceRemnawave
	case !health.ValkeyHealthy:
		return MaintenanceValkey
	default:
		return ""
	}
}

// MaintenanceDecision is what the detector wants to happen next.
type MaintenanceDecision struct {
	// Activate and Clear are mutually exclusive; both false means no change.
	Activate bool
	Clear    bool
	Source   string
	Reason   string
}

// EvaluateMaintenance decides whether an automatic transition is due.
//
// Activation needs failureStreak consecutive unhealthy samples so one timeout
// cannot close an installation. Recovery needs recoveryStreak consecutive
// healthy samples so a flapping dependency does not reopen purchases too early.
// A manual maintenance window is never cleared automatically: an operator who
// turned it on is the only one who turns it off.
func EvaluateMaintenance(current Maintenance, health DependencyHealth, failures, recoveries, failureStreak, recoveryStreak int) MaintenanceDecision {
	if failureStreak < 1 {
		failureStreak = 1
	}
	if recoveryStreak < 1 {
		recoveryStreak = 1
	}
	if !health.Healthy() {
		source := health.firstUnhealthy()
		if current.Active || failures < failureStreak {
			return MaintenanceDecision{}
		}
		return MaintenanceDecision{Activate: true, Source: source, Reason: source + " is unavailable"}
	}
	if !current.Active || current.Source == MaintenanceManual {
		return MaintenanceDecision{}
	}
	if recoveries < recoveryStreak {
		return MaintenanceDecision{}
	}
	return MaintenanceDecision{Clear: true, Source: current.Source, Reason: "dependencies recovered"}
}
