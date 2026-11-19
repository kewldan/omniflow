package commerce

import "testing"

func TestThePlanTagWinsAndATrialFallsBackToTheTrialTag(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                    string
		plan, kind, trial, paid string
		wantTag                 string
		wantManaged             bool
	}{
		{"nothing configured leaves the panel alone", "", "trial", "", "", "", false},
		{"the plan's tag wins", "VIP", "trial", "TRIAL", "PAID", "VIP", true},
		{"a trial carries the trial tag", "", "trial", "TRIAL", "PAID", "TRIAL", true},
		{"a paid plan carries the paid tag", "", "recurring", "TRIAL", "PAID", "PAID", true},
		{"a paid plan with only a trial tag clears it", "", "one_time", "TRIAL", "", "", true},
	}
	for _, testCase := range cases {
		tag, managed := PanelTag(testCase.plan, testCase.kind, testCase.trial, testCase.paid)
		if tag != testCase.wantTag || managed != testCase.wantManaged {
			t.Fatalf("%s: (%q, %v), want (%q, %v)", testCase.name, tag, managed, testCase.wantTag, testCase.wantManaged)
		}
	}
}

func TestTheChangePolicyConstrainsAndResets(t *testing.T) {
	t.Parallel()
	policy := DefaultChangePolicy()
	if upgrade, downgrade := policy.Constrain("extend", "at_expiry"); upgrade != "extend" || downgrade != "at_expiry" {
		t.Fatal("the default policy must leave a version's policies alone")
	}
	if !policy.ResetsTraffic("renewal") || !policy.ResetsTraffic("upgrade") || policy.ResetsTraffic("downgrade") {
		t.Fatal("the default resets are what the code did before the switches")
	}
	policy.UpgradesEnabled = false
	policy.ResetTrafficOnRenewal = false
	policy.ResetDevicesOnRenewal = true
	if upgrade, _ := policy.Constrain("extend", "at_expiry"); upgrade != "forbid" {
		t.Fatal("upgrades switched off must read as forbidden")
	}
	if policy.ResetsTraffic("extension") || !policy.ResetsDevices("renewal") || policy.ResetsDevices("upgrade") {
		t.Fatal("the switches were not applied")
	}
	for mode, want := range map[string]string{"policy": "replace", "carry": "extend", "reset": "replace"} {
		policy.TrialRemainder = mode
		if got := policy.UpgradeFromTrial("replace"); got != want {
			t.Fatalf("%s: %s, want %s", mode, got, want)
		}
	}
}
