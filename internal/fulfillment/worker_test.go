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
