// Package aidraft is the lifecycle of a suggestion an operator has not accepted
// yet.
//
// Every AI feature in Omniflow produces something a person then edits, accepts,
// or throws away, and each of them was inventing its own answer to the same
// four questions: is this generated, what produced it, what changed, and how do
// I undo. One answer is better than six, and the interesting part is that the
// answers are load-bearing rather than cosmetic.
//
// A draft always knows it is generated. The label is a field on the value
// rather than a flag the UI sets, so a surface that forgets to render it is a
// bug in one place instead of a silent misattribution — an operator sending a
// model's words as their own is the failure this prevents.
//
// A draft always knows what produced it. Provider, model, and the time are
// carried with the text, because "why did we tell a customer that in March?"
// is a question asked long after the configuration has changed.
//
// Nothing is accepted implicitly. A revision an operator has not accepted is
// never the thing that gets sent, and acceptance records whether they edited
// it — which is a different decision from pressing send unchanged.
package aidraft

import (
	"errors"
	"strings"
	"time"
)

// State is where a draft is in its life.
//
// The failure states are values rather than errors because the panel has to
// render them: "the provider timed out, retry or write it yourself" is a
// screen, and an error returned up the stack is not.
const (
	// StatePending is a request in flight. It exists so a surface can show
	// progress and offer cancellation rather than freezing.
	StatePending = "pending"
	// StateReady is a suggestion waiting for a person.
	StateReady = "ready"
	// StateAccepted is one a person took, edited or not.
	StateAccepted = "accepted"
	// StateDiscarded is one a person rejected. It is kept rather than deleted,
	// because "we suggested this and the operator said no" is the record that
	// makes a quality review possible.
	StateDiscarded = "discarded"
	// StateFailed is a request that did not produce a suggestion.
	StateFailed = "failed"
	// StateCancelled is one a person stopped.
	StateCancelled = "cancelled"
	// StateUnavailable is the feature being off, over budget, or unconfigured.
	// It is separate from failed because the operator's next action differs:
	// one is "try again", the other is "this is not available, write it
	// yourself".
	StateUnavailable = "unavailable"
)

// Failure codes an operator sees. They are a closed set so a surface can
// translate them, and they never carry a provider's own message: a provider
// error can echo the prompt back.
const (
	FailureTimeout      = "timeout"
	FailureProvider     = "provider_error"
	FailureBudget       = "budget_exhausted"
	FailureDisabled     = "feature_disabled"
	FailureLimit        = "usage_limit_reached"
	FailureUnusable     = "unusable_output"
	FailureCancelled    = "cancelled_by_operator"
	FailureUnconfigured = "not_configured"
)

var (
	// ErrNotAccepted reports an attempt to use a draft nobody accepted.
	ErrNotAccepted = errors.New("this draft has not been accepted")
	// ErrNoRevision reports an undo with nothing to go back to.
	ErrNoRevision = errors.New("there is no earlier revision")
	// ErrAlreadySettled reports an accept or discard on a draft that already
	// has one, which is a double submit rather than a decision.
	ErrAlreadySettled = errors.New("this draft has already been settled")
)

// Provenance is what produced a revision.
//
// It travels with the text rather than beside it, so a revision copied into a
// log or an export cannot lose the answer to "what wrote this?".
type Provenance struct {
	// Generated is false for a revision a person typed. A draft can contain
	// both: the model's suggestion and the operator's edit of it, and telling
	// them apart is the whole point of keeping revisions.
	Generated bool   `json:"generated"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	// Feature names which AI feature produced it, so a usage or quality report
	// can be grouped without joining anything.
	Feature string `json:"feature,omitempty"`
	// Author is the operator, for a revision a person wrote.
	Author string    `json:"author,omitempty"`
	At     time.Time `json:"at"`
}

// Revision is one version of the text.
type Revision struct {
	Number     int        `json:"number"`
	Text       string     `json:"text"`
	Provenance Provenance `json:"provenance"`
}

// Draft is a suggestion and everything that happened to it.
type Draft struct {
	ID      string `json:"id"`
	Feature string `json:"feature"`
	State   string `json:"state"`

	// Revisions are ordered oldest first. The first is what the model produced;
	// later ones are the operator's edits. Keeping the original is what makes a
	// quality review possible after somebody has rewritten it.
	Revisions []Revision `json:"revisions"`

	// Failure explains a failed or unavailable draft in terms an operator can
	// act on.
	Failure string `json:"failure,omitempty"`
	// Retryable separates a failure worth trying again from one that will
	// produce the same answer. Offering retry on a disabled feature trains
	// operators to click it pointlessly.
	Retryable bool `json:"retryable"`

	// Attempts counts how many times this draft has been requested, so a retry
	// loop is visible in the record rather than only in the bill.
	Attempts int `json:"attempts"`

	SettledAt time.Time `json:"settledAt,omitzero"`
	SettledBy string    `json:"settledBy,omitempty"`
	// Edited records that the accepted text differs from what was generated. It
	// is separate from accepted because an operator who rewrote half of it made
	// a different decision from one who pressed send.
	Edited bool `json:"edited"`
}

// Pending starts a draft for a request in flight.
func Pending(id, feature string) *Draft {
	return &Draft{ID: id, Feature: feature, State: StatePending, Attempts: 1}
}

// Unavailable builds a draft for a feature that cannot run.
//
// It is a draft rather than an error so the surface renders the same component
// it always does, with the button replaced by an explanation. A feature that
// vanishes when it is off teaches operators that it is unreliable.
func Unavailable(id, feature, failure string) *Draft {
	return &Draft{
		ID: id, Feature: feature, State: StateUnavailable,
		Failure: failure, Retryable: false,
	}
}

// Ready records a generated suggestion.
func (draft *Draft) Ready(text string, provenance Provenance) {
	provenance.Generated = true
	if provenance.Feature == "" {
		provenance.Feature = draft.Feature
	}
	draft.Revisions = append(draft.Revisions, Revision{
		Number: len(draft.Revisions) + 1, Text: strings.TrimSpace(text),
		Provenance: provenance,
	})
	draft.State = StateReady
	draft.Failure, draft.Retryable = "", false
}

// Failed records a request that produced nothing.
func (draft *Draft) Failed(failure string) {
	draft.State = StateFailed
	draft.Failure = failure
	// Only the transient failures are worth another request. Retrying a
	// disabled feature or an exhausted budget spends an operator's attention on
	// the same answer.
	switch failure {
	case FailureTimeout, FailureProvider, FailureUnusable:
		draft.Retryable = true
	default:
		draft.Retryable = false
	}
}

// Cancel records an operator stopping a request.
//
// Cancellation is a state rather than a discarded request because the model
// call may already have cost money, and a record that says "we spent this and
// the operator stopped waiting" is more honest than one that says nothing
// happened.
func (draft *Draft) Cancel(operator string) {
	if draft.State != StatePending {
		return
	}
	draft.State = StateCancelled
	draft.Failure = FailureCancelled
	draft.Retryable = true
	draft.SettledBy = operator
}

// Retry starts another attempt, keeping everything that came before.
func (draft *Draft) Retry() error {
	if !draft.Retryable {
		return ErrAlreadySettled
	}
	draft.State = StatePending
	draft.Attempts++
	draft.Failure, draft.Retryable = "", false
	return nil
}

// Edit records an operator's own version.
//
// It appends rather than replacing, which is what makes undo possible and what
// keeps the generated original available for a quality review after somebody
// has rewritten it.
func (draft *Draft) Edit(text, operator string, at time.Time) {
	draft.Revisions = append(draft.Revisions, Revision{
		Number: len(draft.Revisions) + 1, Text: strings.TrimSpace(text),
		Provenance: Provenance{Generated: false, Author: operator, At: at.UTC()},
	})
	draft.State = StateReady
}

// Undo drops the most recent revision.
//
// It refuses to remove the last one: a draft with no revisions is not an
// earlier state, it is a different object, and "undo" that empties the editor
// is a data-loss button wearing a friendly label.
func (draft *Draft) Undo() error {
	if len(draft.Revisions) <= 1 {
		return ErrNoRevision
	}
	draft.Revisions = draft.Revisions[:len(draft.Revisions)-1]
	return nil
}

// Current returns the newest revision.
func (draft *Draft) Current() (Revision, bool) {
	if len(draft.Revisions) == 0 {
		return Revision{}, false
	}
	return draft.Revisions[len(draft.Revisions)-1], true
}

// Original returns the generated revision this draft started from.
func (draft *Draft) Original() (Revision, bool) {
	for _, revision := range draft.Revisions {
		if revision.Provenance.Generated {
			return revision, true
		}
	}
	return Revision{}, false
}

// Accept settles a draft.
//
// The text is taken from the draft rather than from the caller, so a surface
// cannot accept one thing and send another. Whether it was edited is derived
// rather than reported, for the same reason.
func (draft *Draft) Accept(operator string, at time.Time) (Revision, error) {
	if draft.State == StateAccepted || draft.State == StateDiscarded {
		return Revision{}, ErrAlreadySettled
	}
	current, present := draft.Current()
	if !present {
		return Revision{}, ErrNotAccepted
	}
	original, generated := draft.Original()

	draft.State = StateAccepted
	draft.SettledAt = at.UTC()
	draft.SettledBy = operator
	draft.Edited = generated && original.Text != current.Text
	return current, nil
}

// Discard settles a draft as rejected. It is kept rather than deleted, because
// "we suggested this and the operator said no" is what makes a quality review
// possible.
func (draft *Draft) Discard(operator string, at time.Time) error {
	if draft.State == StateAccepted || draft.State == StateDiscarded {
		return ErrAlreadySettled
	}
	draft.State = StateDiscarded
	draft.SettledAt = at.UTC()
	draft.SettledBy = operator
	return nil
}

// Sendable returns the text a caller may act on, or refuses.
//
// Every path that sends a generated reply goes through here, so "an authorised
// operator must explicitly send it" is enforced by the type rather than by each
// caller remembering.
func (draft *Draft) Sendable() (string, error) {
	if draft.State != StateAccepted {
		return "", ErrNotAccepted
	}
	current, present := draft.Current()
	if !present {
		return "", ErrNotAccepted
	}
	return current.Text, nil
}

// Change is one line of a side-by-side comparison.
type Change struct {
	// Kind is "same", "added", or "removed".
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// Diff compares two revisions line by line.
//
// It is a line diff rather than a word diff because the material is short
// operator-facing prose, and a word diff of a rewritten paragraph produces
// confetti that is harder to read than the two versions side by side.
func Diff(before, after string) []Change {
	left := splitLines(before)
	right := splitLines(after)

	// Longest common subsequence over lines. The inputs are a support reply or
	// a campaign message, so the quadratic table is a few thousand cells.
	lengths := make([][]int, len(left)+1)
	for row := range lengths {
		lengths[row] = make([]int, len(right)+1)
	}
	for row := len(left) - 1; row >= 0; row-- {
		for column := len(right) - 1; column >= 0; column-- {
			if left[row] == right[column] {
				lengths[row][column] = lengths[row+1][column+1] + 1
				continue
			}
			lengths[row][column] = max(lengths[row+1][column], lengths[row][column+1])
		}
	}

	changes := make([]Change, 0, len(left)+len(right))
	row, column := 0, 0
	for row < len(left) && column < len(right) {
		switch {
		case left[row] == right[column]:
			changes = append(changes, Change{Kind: "same", Text: left[row]})
			row++
			column++
		case lengths[row+1][column] >= lengths[row][column+1]:
			changes = append(changes, Change{Kind: "removed", Text: left[row]})
			row++
		default:
			changes = append(changes, Change{Kind: "added", Text: right[column]})
			column++
		}
	}
	for ; row < len(left); row++ {
		changes = append(changes, Change{Kind: "removed", Text: left[row]})
	}
	for ; column < len(right); column++ {
		changes = append(changes, Change{Kind: "added", Text: right[column]})
	}
	return changes
}

// DiffAgainstOriginal compares what a person has now with what was generated,
// which is the comparison an operator actually wants before accepting.
func (draft *Draft) DiffAgainstOriginal() []Change {
	original, generated := draft.Original()
	current, present := draft.Current()
	if !generated || !present {
		return nil
	}
	return Diff(original.Text, current.Text)
}

func splitLines(text string) []string {
	normalised := strings.ReplaceAll(text, "\r\n", "\n")
	if strings.TrimSpace(normalised) == "" {
		return nil
	}
	return strings.Split(normalised, "\n")
}
