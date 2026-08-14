package fulfillment

import (
	"testing"

	"github.com/omniflow/omniflow/internal/remnawave"
)

// A pause is a disabled Remnawave user plus a stopped clock, and only Omniflow
// knows about the second half. These are the cases where that asymmetry decides
// whether a customer keeps the days they paid for.
func TestAPausedEntitlementSurvivesReconciliation(t *testing.T) {
	for name, remote := range map[string]remnawave.User{
		// What a paused subscription looks like from Remnawave the moment after
		// it is paused.
		"disabled": {Status: "DISABLED"},
		// And what it looks like a week later, once real time has walked past
		// the expiry the pause froze. Remnawave knows nothing about the pause,
		// so it reports the user as expired — and treating that as the
		// entitlement expiring would undo the feature with a background job.
		"expired": {Status: "EXPIRED"},
	} {
		if got := reconciledStatus("paused", remote); got != "paused" {
			t.Errorf("a paused entitlement whose remote user is %s reconciled to %q; "+
				"the customer just lost the days the pause was preserving", name, got)
		}
	}

	// The dangerous case: somebody re-enabled the user in Remnawave, so the
	// customer is connecting on time nobody is charging for. That is drift and
	// must be reported rather than absorbed.
	if got := reconciledStatus("paused", remnawave.User{Status: "ACTIVE"}); got != "active" {
		t.Errorf("a paused entitlement with an active remote user reconciled to %q, "+
			"want active so the drift is reported", got)
	}
}

// Nothing above may change what reconciliation does to an entitlement that is
// not paused.
func TestReconciliationIsUnchangedForEverythingElse(t *testing.T) {
	for local := range map[string]struct{}{"active": {}, "limited": {}, "disabled": {}, "pending": {}} {
		for status, want := range map[string]string{
			"ACTIVE": "active", "LIMITED": "limited",
			"DISABLED": "disabled", "EXPIRED": "expired", "WHAT": "failed",
		} {
			if got := reconciledStatus(local, remnawave.User{Status: status}); got != want {
				t.Errorf("local %s with remote %s reconciled to %q, want %q",
					local, status, got, want)
			}
		}
	}
}
