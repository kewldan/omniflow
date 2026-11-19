package commerce

// ChangePolicy is the installation-wide half of what a paid change does to a
// subscription. A plan version's own upgrade and downgrade policies still
// apply; these sit above them and are set once, from the panel, for the whole
// catalogue.
type ChangePolicy struct {
	// UpgradesEnabled and DowngradesEnabled turn either kind of plan change
	// off everywhere. Off reads as the version's policy being `forbid`, so the
	// one rule that already refuses a forbidden change refuses this too.
	UpgradesEnabled   bool
	DowngradesEnabled bool
	// ResetTrafficOnRenewal restarts the traffic counter when an extension or
	// a renewal settles; ResetTrafficOnPlanChange does the same for an
	// upgrade.
	ResetTrafficOnRenewal    bool
	ResetTrafficOnPlanChange bool
	// ResetDevicesOnRenewal removes every registered device when an extension
	// or a renewal settles, so the customer reconnects within the new period.
	ResetDevicesOnRenewal bool
	// TrialRemainder decides what a trial's remaining time does when the
	// customer moves to a paid plan: TrialRemainderPolicy follows the paid
	// version's upgrade policy, TrialRemainderCarry adds the paid period on
	// top of it, and TrialRemainderReset starts the paid period now.
	TrialRemainder string
}

// What a trial's remaining time does on a move to a paid plan.
const (
	TrialRemainderPolicy = "policy"
	TrialRemainderCarry  = "carry"
	TrialRemainderReset  = "reset"
)

// DefaultChangePolicy is what the code did before the switches existed, and
// what the seeded row holds.
func DefaultChangePolicy() ChangePolicy {
	return ChangePolicy{
		UpgradesEnabled: true, DowngradesEnabled: true,
		ResetTrafficOnRenewal: true, ResetTrafficOnPlanChange: true,
		TrialRemainder: TrialRemainderPolicy,
	}
}

// Constrain applies the installation switches to a plan's own policies: a
// kind of change that is switched off reads as forbidden.
func (policy ChangePolicy) Constrain(upgradePolicy, downgradePolicy string) (string, string) {
	if !policy.UpgradesEnabled {
		upgradePolicy = "forbid"
	}
	if !policy.DowngradesEnabled {
		downgradePolicy = "forbid"
	}
	return upgradePolicy, downgradePolicy
}

// ResetsTraffic reports whether settling the operation starts the customer's
// traffic period afresh in Remnawave. An extension, a renewal, and an upgrade
// buy a new period, so by default the counter restarts and a LIMITED user
// comes back as ACTIVE; the installation may turn either half off. A purchase
// provisions a new user with nothing to reset, and a downgrade keeps whatever
// the policy says about the current period.
func (policy ChangePolicy) ResetsTraffic(operation string) bool {
	switch operation {
	case "extension", "renewal":
		return policy.ResetTrafficOnRenewal
	case "upgrade":
		return policy.ResetTrafficOnPlanChange
	default:
		return false
	}
}

// ResetsDevices reports whether settling the operation removes the
// subscription's registered devices.
func (policy ChangePolicy) ResetsDevices(operation string) bool {
	switch operation {
	case "extension", "renewal":
		return policy.ResetDevicesOnRenewal
	default:
		return false
	}
}

// PanelTag decides the Remnawave user tag for a subscription on a plan of the
// given kind. A tag the plan names wins; otherwise a trial carries the trial
// tag and anything else the paid tag. Managed is false when the installation
// configured no tag at all, and then the panel's own tag is left alone.
func PanelTag(planTag, planKind, trialTag, paidTag string) (tag string, managed bool) {
	if planTag == "" && trialTag == "" && paidTag == "" {
		return "", false
	}
	switch {
	case planTag != "":
		return planTag, true
	case planKind == "trial":
		return trialTag, true
	default:
		return paidTag, true
	}
}

// UpgradeFromTrial is the upgrade policy a move from a trial to a paid plan
// is scheduled with: the paid version's own, or the installation's choice to
// carry the trial's remaining time over (`extend`) or drop it (`replace`).
func (policy ChangePolicy) UpgradeFromTrial(versionPolicy string) string {
	switch policy.TrialRemainder {
	case TrialRemainderCarry:
		return "extend"
	case TrialRemainderReset:
		return "replace"
	default:
		return versionPolicy
	}
}
