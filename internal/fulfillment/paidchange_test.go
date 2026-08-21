package fulfillment

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// fakeProvisioner records the calls a paid change makes against Remnawave and
// answers reads with whatever status the test set.
type fakeProvisioner struct {
	status string
	calls  []string
}

func (fake *fakeProvisioner) User(context.Context, int64) (remnawave.User, error) {
	fake.calls = append(fake.calls, "read")
	return remnawave.User{ID: 7, Status: fake.status}, nil
}

func (fake *fakeProvisioner) UserByUsername(context.Context, string) (remnawave.User, error) {
	return remnawave.User{}, remnawave.ErrNotFound
}

func (fake *fakeProvisioner) CreateUser(context.Context, remnawave.ProvisionUser) (remnawave.User, error) {
	fake.calls = append(fake.calls, "create")
	return remnawave.User{ID: 7, Status: "ACTIVE"}, nil
}

func (fake *fakeProvisioner) UpdateUser(context.Context, int64, remnawave.ProvisionUser) (remnawave.User, error) {
	fake.calls = append(fake.calls, "update")
	return remnawave.User{ID: 7, Status: fake.status}, nil
}

func (fake *fakeProvisioner) EnableUser(context.Context, int64) error {
	fake.calls = append(fake.calls, "enable")
	fake.status = "ACTIVE"
	return nil
}

func (fake *fakeProvisioner) DisableUser(context.Context, int64) error {
	fake.calls = append(fake.calls, "disable")
	return nil
}

func (fake *fakeProvisioner) ResetUserTraffic(context.Context, int64) error {
	fake.calls = append(fake.calls, "reset")
	if fake.status == "LIMITED" {
		fake.status = "ACTIVE"
	}
	return nil
}

func equalCalls(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

// A paid renewal of a user Remnawave holds as LIMITED or DISABLED has to
// restore access, not only move the expiry. These are the calls that do it.
func TestAPaidChangeResetsTrafficAndReEnablesTheUser(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name        string
		remote      string
		entitlement string
		reset       bool
		wantCalls   []string
		wantStatus  string
	}{
		{"renewal of a limited user resets and re-reads", "LIMITED", "pending", true, []string{"reset", "read"}, "ACTIVE"},
		{"renewal of a disabled user re-enables", "DISABLED", "pending", true, []string{"reset", "enable", "read"}, "ACTIVE"},
		{"purchase onto a disabled user re-enables without a reset", "DISABLED", "pending", false, []string{"enable", "read"}, "ACTIVE"},
		{"an active user needs nothing beyond the update", "ACTIVE", "pending", false, nil, "ACTIVE"},
		{"a paused entitlement stays disabled", "DISABLED", "paused", false, nil, "DISABLED"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fake := &fakeProvisioner{status: testCase.remote}
			worker := NewWorker(nil, fake)
			remote, err := worker.finishPaidChange(ctx, remnawave.User{ID: 7, Status: testCase.remote}, dbgen.Entitlement{Status: testCase.entitlement}, desiredState{ResetTraffic: testCase.reset})
			if err != nil {
				t.Fatalf("finish: %v", err)
			}
			if !equalCalls(fake.calls, testCase.wantCalls) {
				t.Fatalf("calls = %v, want %v", fake.calls, testCase.wantCalls)
			}
			if remote.Status != testCase.wantStatus {
				t.Fatalf("remote status = %q, want %q", remote.Status, testCase.wantStatus)
			}
		})
	}
}

func TestShouldEnableLeavesAPauseAlone(t *testing.T) {
	if !shouldEnable("DISABLED", "pending") || !shouldEnable("disabled", "active") {
		t.Fatal("a disabled user behind a paid change must be re-enabled")
	}
	if shouldEnable("DISABLED", "paused") {
		t.Fatal("a paused entitlement is disabled on purpose and must stay so")
	}
	for _, status := range []string{"ACTIVE", "LIMITED", "EXPIRED"} {
		if shouldEnable(status, "pending") {
			t.Fatalf("a %s user is not re-enabled; Remnawave moves it on its own", status)
		}
	}
}

// Unlimited is a value Remnawave can disagree with. A nil desired limit used
// to be skipped by drift detection, which is how a user left on the previous
// plan's cap after moving to an unlimited one was never reported.
func TestDriftComparisonTreatsNilAsUnlimited(t *testing.T) {
	limit := int64(1024)
	if !trafficLimitAgrees(nil, 0) || trafficLimitAgrees(nil, 1024) {
		t.Fatal("a nil allowance must agree only with a zero remote cap")
	}
	if !trafficLimitAgrees(&limit, 1024) || trafficLimitAgrees(&limit, 0) {
		t.Fatal("a configured allowance must match exactly")
	}
	zero, three := 0, 3
	if !deviceLimitAgrees(nil, nil) || !deviceLimitAgrees(nil, &zero) || deviceLimitAgrees(nil, &three) {
		t.Fatal("a nil device limit must agree with null or zero only")
	}
	if !deviceLimitAgrees(&three, &three) || deviceLimitAgrees(&three, nil) || deviceLimitAgrees(&three, &zero) {
		t.Fatal("a configured device limit must match exactly")
	}
}
