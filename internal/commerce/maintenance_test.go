package commerce

import "testing"

func TestMaintenanceBlocksPurchasesAndFulfillmentOnly(t *testing.T) {
	t.Parallel()
	active := Maintenance{Active: true, Source: MaintenanceRemnawave}
	if !active.Blocks(ActionPurchase) || !active.Blocks(ActionFulfillment) {
		t.Fatal("maintenance must block purchases and fulfillment")
	}
	// Browsing stays open so a customer can read the notice, their orders, and
	// support instead of hitting a dead end.
	if active.Blocks(ActionBrowse) {
		t.Fatal("maintenance must not block read-only screens")
	}
	if (Maintenance{}).Blocks(ActionPurchase) {
		t.Fatal("an inactive record must block nothing")
	}
}

func TestAutomaticActivationNeedsAFullFailureStreak(t *testing.T) {
	t.Parallel()
	unhealthy := DependencyHealth{RemnawaveHealthy: false, DatabaseHealthy: true, ValkeyHealthy: true}
	if decision := EvaluateMaintenance(Maintenance{}, unhealthy, 2, 0, 3, 3); decision.Activate {
		t.Fatal("two failures must not close the installation")
	}
	decision := EvaluateMaintenance(Maintenance{}, unhealthy, 3, 0, 3, 3)
	if !decision.Activate || decision.Source != MaintenanceRemnawave {
		t.Fatalf("expected activation from Remnawave, got %+v", decision)
	}
}

func TestAutomaticRecoveryNeedsAFullHealthyStreak(t *testing.T) {
	t.Parallel()
	healthy := DependencyHealth{RemnawaveHealthy: true, DatabaseHealthy: true, ValkeyHealthy: true}
	current := Maintenance{Active: true, Source: MaintenanceRemnawave}
	if decision := EvaluateMaintenance(current, healthy, 0, 2, 3, 3); decision.Clear {
		t.Fatal("a flapping dependency must not reopen purchases early")
	}
	if decision := EvaluateMaintenance(current, healthy, 0, 3, 3, 3); !decision.Clear {
		t.Fatal("a full healthy streak must clear an automatic window")
	}
}

// An operator who turned maintenance on is the only one who turns it off.
func TestManualMaintenanceIsNeverClearedAutomatically(t *testing.T) {
	t.Parallel()
	healthy := DependencyHealth{RemnawaveHealthy: true, DatabaseHealthy: true, ValkeyHealthy: true}
	current := Maintenance{Active: true, Source: MaintenanceManual}
	if decision := EvaluateMaintenance(current, healthy, 0, 99, 1, 1); decision.Clear {
		t.Fatal("a manual window must survive automatic recovery")
	}
}

func TestAlreadyActiveMaintenanceIsNotReactivated(t *testing.T) {
	t.Parallel()
	unhealthy := DependencyHealth{DatabaseHealthy: false, RemnawaveHealthy: true, ValkeyHealthy: true}
	current := Maintenance{Active: true, Source: MaintenanceDatabase}
	if decision := EvaluateMaintenance(current, unhealthy, 10, 0, 3, 3); decision.Activate {
		t.Fatal("an open window must not be reactivated on every sample")
	}
}

func TestDatabaseOutranksOtherSourcesInTheReport(t *testing.T) {
	t.Parallel()
	both := DependencyHealth{DatabaseHealthy: false, RemnawaveHealthy: false, ValkeyHealthy: false}
	decision := EvaluateMaintenance(Maintenance{}, both, 1, 0, 1, 1)
	if decision.Source != MaintenanceDatabase {
		t.Fatalf("expected the database to be named first, got %q", decision.Source)
	}
}
