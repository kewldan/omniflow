package fulfillment

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/remnawave"
)

func TestBackoffIsBounded(t *testing.T) {
	if got := backoff(0); got != 5*time.Second {
		t.Fatalf("first backoff = %s", got)
	}
	if got := backoff(50); got != 1280*time.Second {
		t.Fatalf("bounded backoff = %s", got)
	}
}

func TestFulfillmentErrorsAreSafeAndClassified(t *testing.T) {
	if got := classifyError(remnawave.ErrNotFound); got != "remote_user_not_found" {
		t.Fatalf("not-found classification = %q", got)
	}
	if got := classifyError(&remnawave.APIError{StatusCode: 503}); got != "remnawave_http_503" {
		t.Fatalf("API classification = %q", got)
	}
	if got := classifyError(errors.New("token=secret")); got != "remnawave_unavailable" || strings.Contains(got, "secret") {
		t.Fatalf("unsafe generic classification = %q", got)
	}
}

func TestDesiredSummaryContainsNoSquadIdentifiers(t *testing.T) {
	traffic := int64(1024)
	deviceLimit := 3
	summary := string(safeDesiredSummary(desiredState{
		EndsAt:                time.Unix(1, 0).UTC(),
		TrafficAllowanceBytes: &traffic,
		DeviceLimit:           &deviceLimit,
		SquadIDs:              []string{"squad-101", "squad-202"},
	}))
	if strings.Contains(summary, "squad-101") || strings.Contains(summary, "squad-202") || !strings.Contains(summary, `"squadCount":2`) {
		t.Fatalf("unsafe fulfillment summary: %s", summary)
	}
}

// What Remnawave is told to expire is the paid end plus the plan's grace. The
// paid end itself is untouched: it is what renewal arithmetic and reminders
// read, and pushing it without the grace is how a promised grace period turned
// out to be cosmetic.
func TestDesiredStateCarriesTheGraceOnlyInTheRemoteExpiry(t *testing.T) {
	endsAt := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	desired := desiredState{EndsAt: endsAt}.withGrace(48 * time.Hour)
	if !desired.EndsAt.Equal(endsAt) {
		t.Fatalf("the paid end moved to %s", desired.EndsAt)
	}
	if !desired.remoteExpireAt.Equal(endsAt.Add(48 * time.Hour)) {
		t.Fatalf("the remote expiry is %s, want the paid end plus the grace", desired.remoteExpireAt)
	}
	if plain := (desiredState{EndsAt: endsAt}).withGrace(0); !plain.remoteExpireAt.Equal(endsAt) {
		t.Fatalf("a plan without grace must push the paid end, got %s", plain.remoteExpireAt)
	}
	if summary := string(safeDesiredSummary(desired)); !strings.Contains(summary, `"remoteExpireAt":"2026-09-03T00:00:00Z"`) {
		t.Fatalf("the history summary must record what was pushed: %s", summary)
	}
}

func TestSquadComparisonIgnoresProviderOrdering(t *testing.T) {
	if !sameStringSet([]string{"a", "b"}, []string{"b", "a"}) || sameStringSet([]string{"a"}, []string{"b"}) {
		t.Fatal("unexpected squad-set comparison")
	}
}

func TestObservedEntitlementStatusIsExplicit(t *testing.T) {
	for remote, expected := range map[string]string{
		"ACTIVE": "active", "LIMITED": "limited", "DISABLED": "disabled", "EXPIRED": "expired", "UNKNOWN": "failed",
	} {
		if got := observedEntitlementStatus(remnawave.User{Status: remote}); got != expected {
			t.Fatalf("%s mapped to %s", remote, got)
		}
	}
}
