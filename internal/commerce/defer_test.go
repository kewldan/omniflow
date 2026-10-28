package commerce

import "testing"

// Only an at_expiry downgrade waits behind the current entitlement; every
// other change takes effect now, which is why it alone has to resume a paused
// subscription before it is scheduled.
func TestOnlyAnAtExpiryDowngradeDefersBehindTheCurrentEntitlement(t *testing.T) {
	if !DefersBehindCurrent("downgrade", "at_expiry") {
		t.Fatal("an at_expiry downgrade waits for the current period")
	}
	for _, operation := range []string{"purchase", "extension", "renewal", "upgrade"} {
		if DefersBehindCurrent(operation, "at_expiry") {
			t.Fatalf("%s takes effect now", operation)
		}
	}
	if DefersBehindCurrent("downgrade", "immediate") {
		t.Fatal("an immediate downgrade takes effect now")
	}
}
