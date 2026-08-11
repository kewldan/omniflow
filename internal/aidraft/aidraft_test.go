package aidraft

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func at(minute int) time.Time {
	return time.Date(2026, 8, 11, 9, minute, 0, 0, time.UTC)
}

func generated(text string) *Draft {
	draft := Pending("d-1", "support_reply")
	draft.Ready(text, Provenance{Provider: "acme", Model: "acme-1", At: at(0)})
	return draft
}

// An operator sending a model's words as their own is the failure the label
// prevents, so it is a field on the value rather than a flag the UI sets.
func TestAGeneratedRevisionCarriesItsProvenance(t *testing.T) {
	draft := generated("Your subscription renews on Friday.")
	revision, present := draft.Current()
	if !present {
		t.Fatal("a ready draft has no revision")
	}
	if !revision.Provenance.Generated {
		t.Fatal("a generated revision is not marked as generated")
	}
	if revision.Provenance.Provider != "acme" || revision.Provenance.Model != "acme-1" {
		t.Fatalf("the provider and model were lost: %+v", revision.Provenance)
	}
	if revision.Provenance.Feature != "support_reply" {
		t.Fatalf("the feature was not recorded: %+v", revision.Provenance)
	}
}

// An operator's edit is a revision too, and it is not generated. Telling the
// two apart is the whole point of keeping revisions.
func TestAnEditIsARevisionAndIsNotGenerated(t *testing.T) {
	draft := generated("Your subscription renews on Friday.")
	draft.Edit("Your subscription renews on Friday. Let me know if that is wrong.",
		"op-1", at(5))

	if len(draft.Revisions) != 2 {
		t.Fatalf("the edit replaced the original instead of appending: %d", len(draft.Revisions))
	}
	current, _ := draft.Current()
	if current.Provenance.Generated || current.Provenance.Author != "op-1" {
		t.Fatalf("the edit was attributed to the model: %+v", current.Provenance)
	}
	original, found := draft.Original()
	if !found || !strings.HasSuffix(original.Text, "Friday.") {
		t.Fatalf("the generated original was lost: %+v", original)
	}
}

// A surface cannot accept one thing and send another, so the text comes from
// the draft.
func TestNothingIsSendableUntilItIsAccepted(t *testing.T) {
	draft := generated("A suggestion.")
	if _, err := draft.Sendable(); !errors.Is(err, ErrNotAccepted) {
		t.Fatalf("an unaccepted draft was sendable: %v", err)
	}

	accepted, err := draft.Accept("op-1", at(6))
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	text, err := draft.Sendable()
	if err != nil || text != accepted.Text {
		t.Fatalf("the accepted text is not what is sendable: %q %q %v", text, accepted.Text, err)
	}
	if draft.Edited {
		t.Fatal("an unedited acceptance was recorded as edited")
	}
}

// Pressing send unchanged and rewriting half of it are different decisions.
func TestAcceptanceRecordsWhetherItWasEdited(t *testing.T) {
	draft := generated("A suggestion.")
	draft.Edit("My own words entirely.", "op-1", at(5))
	if _, err := draft.Accept("op-1", at(6)); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if !draft.Edited {
		t.Fatal("an edited acceptance was recorded as unedited")
	}
	if draft.SettledBy != "op-1" || draft.SettledAt.IsZero() {
		t.Fatalf("the settlement was not attributed: %+v", draft)
	}
}

// A double submit is not a second decision.
func TestASettledDraftCannotBeSettledAgain(t *testing.T) {
	draft := generated("A suggestion.")
	if _, err := draft.Accept("op-1", at(6)); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if _, err := draft.Accept("op-2", at(7)); !errors.Is(err, ErrAlreadySettled) {
		t.Fatalf("a second acceptance was allowed: %v", err)
	}
	if err := draft.Discard("op-2", at(7)); !errors.Is(err, ErrAlreadySettled) {
		t.Fatalf("an accepted draft was discarded: %v", err)
	}
}

// "Undo" that empties the editor is a data-loss button wearing a friendly
// label.
func TestUndoWillNotRemoveTheLastRevision(t *testing.T) {
	draft := generated("A suggestion.")
	if err := draft.Undo(); !errors.Is(err, ErrNoRevision) {
		t.Fatalf("undo emptied a single-revision draft: %v", err)
	}

	draft.Edit("Edited once.", "op-1", at(5))
	draft.Edit("Edited twice.", "op-1", at(6))
	if err := draft.Undo(); err != nil {
		t.Fatalf("undo: %v", err)
	}
	current, _ := draft.Current()
	if current.Text != "Edited once." {
		t.Fatalf("undo went to the wrong revision: %q", current.Text)
	}
	if err := draft.Undo(); err != nil {
		t.Fatalf("undo back to the original: %v", err)
	}
	if err := draft.Undo(); !errors.Is(err, ErrNoRevision) {
		t.Fatalf("undo removed the generated original: %v", err)
	}
}

// Offering retry on a disabled feature trains operators to click it
// pointlessly.
func TestOnlyTransientFailuresAreRetryable(t *testing.T) {
	for failure, retryable := range map[string]bool{
		FailureTimeout:      true,
		FailureProvider:     true,
		FailureUnusable:     true,
		FailureBudget:       false,
		FailureDisabled:     false,
		FailureLimit:        false,
		FailureUnconfigured: false,
	} {
		draft := Pending("d-1", "support_reply")
		draft.Failed(failure)
		if draft.Retryable != retryable {
			t.Fatalf("%s: retryable was %v", failure, draft.Retryable)
		}
		if draft.State != StateFailed {
			t.Fatalf("%s: state was %q", failure, draft.State)
		}
	}
}

// A feature that vanishes when it is off teaches operators that it is
// unreliable, so "unavailable" is a draft rather than a missing component.
func TestAnUnavailableFeatureIsStillADraft(t *testing.T) {
	draft := Unavailable("d-1", "copilot", FailureDisabled)
	if draft.State != StateUnavailable || draft.Retryable {
		t.Fatalf("an unavailable draft offered a retry: %+v", draft)
	}
	if _, err := draft.Sendable(); !errors.Is(err, ErrNotAccepted) {
		t.Fatalf("an unavailable draft was sendable: %v", err)
	}
}

// A record that says "we spent this and the operator stopped waiting" is more
// honest than one that says nothing happened.
func TestCancellationIsRecordedAndRetryable(t *testing.T) {
	draft := Pending("d-1", "support_reply")
	draft.Cancel("op-1")
	if draft.State != StateCancelled || draft.SettledBy != "op-1" {
		t.Fatalf("the cancellation was not recorded: %+v", draft)
	}
	if err := draft.Retry(); err != nil {
		t.Fatalf("a cancelled draft could not be retried: %v", err)
	}
	if draft.Attempts != 2 || draft.State != StatePending {
		t.Fatalf("the retry was not counted: %+v", draft)
	}
}

// A retry loop should be visible in the record rather than only in the bill.
func TestRetryIsRefusedWhenItWouldProduceTheSameAnswer(t *testing.T) {
	draft := Pending("d-1", "support_reply")
	draft.Failed(FailureDisabled)
	if err := draft.Retry(); !errors.Is(err, ErrAlreadySettled) {
		t.Fatalf("a pointless retry was allowed: %v", err)
	}
	if draft.Attempts != 1 {
		t.Fatalf("a refused retry was counted: %d", draft.Attempts)
	}
}

// The comparison an operator wants before accepting is against what was
// generated, not against the previous edit.
func TestTheDiffComparesAgainstWhatWasGenerated(t *testing.T) {
	draft := generated("Hello there.\nYour plan renews on Friday.\nThanks.")
	draft.Edit("Hello there.\nYour plan renews this Friday.\nThanks for your patience.",
		"op-1", at(5))

	changes := draft.DiffAgainstOriginal()
	kinds := map[string]int{}
	for _, change := range changes {
		kinds[change.Kind]++
	}
	if kinds["same"] != 1 {
		t.Fatalf("the unchanged line was not recognised: %+v", changes)
	}
	if kinds["removed"] != 2 || kinds["added"] != 2 {
		t.Fatalf("the changed lines were not paired: %+v", changes)
	}
}

// Identical text produces no changes, so a diff view can say "no edits" rather
// than rendering an empty box.
func TestAnUneditedDraftHasAnEmptyDiff(t *testing.T) {
	draft := generated("One line.\nAnother line.")
	for _, change := range draft.DiffAgainstOriginal() {
		if change.Kind != "same" {
			t.Fatalf("an unedited draft reported a change: %+v", change)
		}
	}
}

// A diff between something and nothing is still readable.
func TestDiffHandlesEmptySides(t *testing.T) {
	if changes := Diff("", "one\ntwo"); len(changes) != 2 {
		t.Fatalf("an insertion from empty was not rendered: %+v", changes)
	}
	if changes := Diff("one\ntwo", ""); len(changes) != 2 {
		t.Fatalf("a deletion to empty was not rendered: %+v", changes)
	}
	if changes := Diff("", ""); len(changes) != 0 {
		t.Fatalf("two empty sides produced changes: %+v", changes)
	}
}
