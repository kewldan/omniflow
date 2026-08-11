package commerce

import (
	"errors"
	"time"
)

// ErrTopUpRejected wraps every reason a wallet top-up is refused. Callers match
// on it and show the stable machine reason it carries.
var ErrTopUpRejected = errors.New("wallet top-up rejected")

// TopUpLimits are the operator-configured bounds on customer-initiated wallet
// funding. Zero values disable the corresponding bound, except MinimumMinor,
// which always keeps a top-up strictly positive.
type TopUpLimits struct {
	// Enabled turns customer-initiated top-up on. Everything else is ignored
	// while it is false.
	Enabled bool
	// Presets are the one-tap amounts offered before free entry, in ascending
	// order and in minor units.
	Presets []int64
	// MinimumMinor and MaximumMinor bound a single top-up. A zero maximum means
	// no per-top-up ceiling.
	MinimumMinor int64
	MaximumMinor int64
	// WindowLimitMinor bounds how much a customer may credit within Window. A
	// zero limit disables the rolling-window control.
	WindowLimitMinor int64
	Window           time.Duration
}

// Top-up rejection reasons. They are stable strings so the bot, the API, and
// the documentation can all name the same outcome.
const (
	TopUpDisabled       = "topup_disabled"
	TopUpBelowMinimum   = "topup_below_minimum"
	TopUpAboveMaximum   = "topup_above_maximum"
	TopUpWindowExceeded = "topup_window_exceeded"
	TopUpInvalidAmount  = "topup_invalid_amount"
)

// Validate checks one requested top-up against the configured limits.
// creditedInWindow is how much the customer has already credited inside Window.
// It returns the stable rejection reason together with ErrTopUpRejected, or
// "accepted" and a nil error.
func (limits TopUpLimits) Validate(amountMinor, creditedInWindow int64) (string, error) {
	if !limits.Enabled {
		return TopUpDisabled, ErrTopUpRejected
	}
	if amountMinor <= 0 {
		return TopUpInvalidAmount, ErrTopUpRejected
	}
	if amountMinor < limits.Minimum() {
		return TopUpBelowMinimum, ErrTopUpRejected
	}
	if limits.MaximumMinor > 0 && amountMinor > limits.MaximumMinor {
		return TopUpAboveMaximum, ErrTopUpRejected
	}
	if limits.WindowLimitMinor > 0 && limits.Window > 0 {
		if creditedInWindow < 0 {
			creditedInWindow = 0
		}
		if creditedInWindow+amountMinor > limits.WindowLimitMinor {
			return TopUpWindowExceeded, ErrTopUpRejected
		}
	}
	return "accepted", nil
}

// Minimum is the effective floor for a single top-up. It is never below one
// minor unit, so a zero-amount top-up can never be created.
func (limits TopUpLimits) Minimum() int64 {
	if limits.MinimumMinor > 1 {
		return limits.MinimumMinor
	}
	return 1
}

// OfferedPresets returns the preset amounts that are actually payable under the
// current limits, so the bot never renders a button it would then refuse.
func (limits TopUpLimits) OfferedPresets(creditedInWindow int64) []int64 {
	offered := make([]int64, 0, len(limits.Presets))
	for _, preset := range limits.Presets {
		if _, err := limits.Validate(preset, creditedInWindow); err == nil {
			offered = append(offered, preset)
		}
	}
	return offered
}

// TopUpSettlement is the result of applying a provider payment to a top-up
// order. Omniflow credits exactly what the provider settled, so an overpayment
// or an underpayment is resolved in the ledger and never leaves the order in a
// state that contradicts the money received.
type TopUpSettlement struct {
	RequestedMinor int64
	ReceivedMinor  int64
	CreditedMinor  int64
	Classification string
}

// SettleTopUp classifies a top-up payment and reports how much to credit.
func SettleTopUp(requestedMinor, receivedMinor int64) (TopUpSettlement, error) {
	if requestedMinor <= 0 || receivedMinor < 0 {
		return TopUpSettlement{}, ErrInvalidAmount
	}
	settlement := TopUpSettlement{RequestedMinor: requestedMinor, ReceivedMinor: receivedMinor, CreditedMinor: receivedMinor, Classification: "paid"}
	switch {
	case receivedMinor > requestedMinor:
		settlement.Classification = "overpayment"
	case receivedMinor < requestedMinor:
		settlement.Classification = "underpayment"
	}
	return settlement, nil
}
