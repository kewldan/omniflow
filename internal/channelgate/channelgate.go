// Package channelgate holds the rules for mandatory channel subscription.
//
// The mechanism can stop a purchase and take away access somebody has paid for,
// so the rules that govern it live here — with no database, no Telegram client,
// and unit tests — rather than being spread through the bot and a worker.
//
// Three of them are load-bearing.
//
// An unknown answer is never treated as absence. Telegram being unreachable is
// not the customer having left, and suspending people because of an outage is
// the failure mode this mechanism has that nothing else in the product does.
//
// Leaving is warned about before it costs anything. The common reason somebody
// leaves a channel is that they did not realise it was load-bearing, and a
// grace period costs the operator a few days of one person's membership while
// its absence costs a paying customer their access without warning.
//
// Rejoining restores automatically. A customer who fixes the problem should not
// have to ask somebody to undo the consequence.
package channelgate

import "time"

// Membership states. `Unknown` is deliberately distinct from `Absent`.
const (
	StateMember  = "member"
	StateAbsent  = "absent"
	StateUnknown = "unknown"
)

// Enforcement states for one customer.
const (
	Compliant = "compliant"
	Warned    = "warned"
	Suspended = "suspended"
	Exempt    = "exempt"
)

// Channel is one required channel as the rules see it.
type Channel struct {
	ID                   string
	Enabled              bool
	RequireForPurchase   bool
	RequireForActivation bool
}

// Membership is what the last check saw for one channel.
type Membership struct {
	ChannelID string
	State     string
	CheckedAt time.Time
}

// Status is the customer's position against the whole rule set.
type Status struct {
	// Missing lists the channels the customer must join and has not. It is
	// empty when they comply, and it is what the bot renders — a customer told
	// "you must join a channel" without being told which one cannot act on it.
	Missing []string
	// Unknown lists channels the check could not answer for. They never block
	// anything: an unreachable Telegram must not gate a purchase.
	Unknown []string
	// Exempt reports that the rule does not apply to this customer at all.
	Exempt bool
}

// Compliant reports whether the customer may proceed.
func (status Status) Compliant() bool { return status.Exempt || len(status.Missing) == 0 }

// Evaluate compares a customer's memberships against the required channels.
//
// `purpose` selects which requirement applies: gating a purchase and gating
// activation are separate promises, because one asks somebody to join before
// they pay and the other takes access from somebody who already did.
func Evaluate(
	channels []Channel, memberships []Membership, purpose string, exempt bool,
) Status {
	status := Status{Missing: []string{}, Unknown: []string{}, Exempt: exempt}
	if exempt {
		return status
	}
	seen := make(map[string]string, len(memberships))
	for _, membership := range memberships {
		seen[membership.ChannelID] = membership.State
	}

	for _, channel := range channels {
		if !channel.Enabled || !required(channel, purpose) {
			continue
		}
		switch seen[channel.ID] {
		case StateMember:
		case StateAbsent:
			status.Missing = append(status.Missing, channel.ID)
		default:
			// No record and an explicit unknown are the same thing: nobody has
			// established that this customer is absent, so nothing is blocked.
			status.Unknown = append(status.Unknown, channel.ID)
		}
	}
	return status
}

// Purposes a requirement can apply to.
const (
	PurposePurchase   = "purchase"
	PurposeActivation = "activation"
)

func required(channel Channel, purpose string) bool {
	switch purpose {
	case PurposePurchase:
		return channel.RequireForPurchase
	case PurposeActivation:
		return channel.RequireForActivation
	default:
		return channel.RequireForPurchase || channel.RequireForActivation
	}
}

// Transition is what should happen to a customer's enforcement state.
type Transition struct {
	State string
	// GraceUntil is set when the customer has just been warned.
	GraceUntil *time.Time
	// Warn is true when a notification should be sent. It fires once per lapse
	// rather than on every check, because a daily reminder to rejoin a channel
	// is how a customer learns to ignore the bot.
	Warn bool
	// Suspend and Restore are the two consequential actions, and they are
	// separate booleans rather than inferred from the state so the caller
	// cannot act on one while recording the other.
	Suspend bool
	Restore bool
	// Changed reports whether anything differs from the current state.
	Changed bool
}

// Next decides what a check result means for a customer.
//
// `current` is their recorded enforcement state, `graceUntil` the clock if one
// is running, and `grace` the operator's configured window.
func Next(
	status Status, current string, graceUntil *time.Time, grace time.Duration, now time.Time,
) Transition {
	if status.Exempt {
		return Transition{State: Exempt, Restore: current == Suspended, Changed: current != Exempt}
	}

	if status.Compliant() {
		// Rejoining restores automatically. A customer who fixed the problem
		// should not have to ask somebody to undo the consequence.
		return Transition{
			State:   Compliant,
			Restore: current == Suspended,
			Changed: current != Compliant,
		}
	}

	switch current {
	case Suspended:
		// Already suspended and still not a member: nothing changes, and in
		// particular no second suspension is recorded.
		return Transition{State: Suspended, Changed: false}

	case Warned:
		if graceUntil != nil && now.Before(*graceUntil) {
			return Transition{State: Warned, GraceUntil: graceUntil, Changed: false}
		}
		return Transition{State: Suspended, Suspend: true, Changed: true}

	default:
		if grace <= 0 {
			// A zero grace period suspends at once. It is a legitimate
			// configuration, and it still sends the warning so the customer
			// knows why.
			return Transition{State: Suspended, Warn: true, Suspend: true, Changed: true}
		}
		until := now.Add(grace)
		return Transition{State: Warned, GraceUntil: &until, Warn: true, Changed: true}
	}
}

// StaleBefore is the instant a cached membership stops being trusted.
//
// Verification results are cached because getChatMember is a rate-limited call
// against a third party, and re-asking on every screen would make the bot slower
// for everybody in order to enforce a marketing rule.
func StaleBefore(now time.Time, recheck time.Duration) time.Time {
	if recheck <= 0 {
		recheck = DefaultRecheck
	}
	return now.Add(-recheck)
}

const (
	// DefaultRecheck is how long a membership answer is trusted.
	DefaultRecheck = 6 * time.Hour
	// DefaultGrace is how long a customer keeps access after leaving.
	//
	// Three days spans a weekend, which is when somebody is most likely to tidy
	// up their channel list and least likely to notice the consequence.
	DefaultGrace = 72 * time.Hour
)
