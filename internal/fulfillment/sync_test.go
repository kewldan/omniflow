package fulfillment

import (
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/remnawave"
)

func TestCompareFieldsMatchesDrift(t *testing.T) {
	expiry := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	limit := int64(10 << 30)
	devices := 3
	desired := desiredState{
		TrafficAllowanceBytes: &limit, DeviceLimit: &devices,
		SquadIDs: []string{"a"}, remoteExpireAt: expiry,
	}
	remote := remnawave.User{
		Status: "ACTIVE", ExpireAt: expiry.Add(time.Hour), TrafficLimitBytes: limit,
		HWIDDeviceLimit: &devices,
	}
	fields := compareFields(desired, "active", remote)
	mismatched := map[string]bool{}
	for _, field := range fields {
		if !field.Matches {
			mismatched[field.Field] = true
		}
	}
	if len(fields) != 5 || !mismatched["expiry"] || !mismatched["squads"] || len(mismatched) != 2 {
		t.Fatalf("unexpected comparison: %+v", fields)
	}
	drifts := driftsBetween(desired, "active", remote)
	if len(drifts) != 2 {
		t.Fatalf("drift must be the mismatching fields, got %d", len(drifts))
	}
}

func TestSyncFollowUp(t *testing.T) {
	status := func(local, remote string, matches bool) SyncPreview {
		return SyncPreview{
			EntitlementStatus: local, RemoteExists: true,
			Fields: []SyncField{{Field: "status", Omniflow: local, Remnawave: remote, Matches: matches}},
		}
	}
	cases := []struct {
		name    string
		preview SyncPreview
		want    string
	}{
		{"in sync", status("active", "active", true), ""},
		{"disabled remotely", status("active", "disabled", false), "enable"},
		{"enabled remotely while paused", status("paused", "active", false), "disable"},
		{"expired remotely", status("active", "expired", false), ""},
		{"no remote user", SyncPreview{EntitlementStatus: "active"}, ""},
	}
	for _, item := range cases {
		if got := SyncFollowUp(item.preview); got != item.want {
			t.Errorf("%s: got %q, want %q", item.name, got, item.want)
		}
	}
}

// TestExpiryComparesAtRemnawavePrecision: Remnawave keeps milliseconds, so a
// stored microsecond expiry that comes back truncated is not drift.
func TestExpiryComparesAtRemnawavePrecision(t *testing.T) {
	stored := time.Date(2026, 11, 22, 9, 3, 19, 508415000, time.UTC)
	desired := desiredState{remoteExpireAt: stored}
	remote := remnawave.User{Status: "ACTIVE", ExpireAt: stored.Truncate(time.Millisecond)}
	for _, field := range compareFields(desired, "active", remote) {
		if field.Field == "expiry" && !field.Matches {
			t.Fatalf("a millisecond-truncated expiry must match: %+v", field)
		}
	}
	remote.ExpireAt = stored.Add(time.Millisecond)
	for _, field := range compareFields(desired, "active", remote) {
		if field.Field == "expiry" && field.Matches {
			t.Fatalf("a millisecond difference is drift: %+v", field)
		}
	}
}
