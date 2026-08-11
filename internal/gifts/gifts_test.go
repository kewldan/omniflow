package gifts

import (
	"bytes"
	"errors"
	"regexp"
	"testing"
	"time"
)

var hintPattern = regexp.MustCompile(`^[A-Z0-9]{4}$`)

func TestNewCodeMatchesWhatTheDatabaseAccepts(t *testing.T) {
	seen := make(map[string]struct{}, 256)
	for range 256 {
		code, hint, err := NewCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != CodeLength {
			t.Fatalf("expected %d characters, got %d", CodeLength, len(code))
		}
		if !hintPattern.MatchString(hint) {
			t.Fatalf("hint %q does not satisfy the gifts.code_hint constraint", hint)
		}
		if hint != code[CodeLength-4:] {
			t.Fatalf("hint %q is not the tail of %q", hint, code)
		}
		if _, duplicate := seen[code]; duplicate {
			t.Fatalf("generated the same code twice: %q", code)
		}
		seen[code] = struct{}{}

		// A generated code must survive its own normalisation, or a customer who
		// types it back exactly as printed would be refused.
		if normalized, err := Normalize(code); err != nil || normalized != code {
			t.Fatalf("Normalize(%q) = %q, %v", code, normalized, err)
		}
	}
}

func TestNormalizeForgivesTheCharactersPeopleActuallyConfuse(t *testing.T) {
	// A code with every substitution applied in reverse, plus the separators a
	// person adds when copying by hand.
	original := "0123456789ABCDEF"
	for _, spelling := range []string{
		"0123456789abcdef",
		"OI23456789ABCDEF",
		"OL23456789ABCDEF",
		"0123-4567-89AB-CDEF",
		"  0123 4567 89AB CDEF  ",
	} {
		normalized, err := Normalize(spelling)
		if err != nil {
			t.Fatalf("Normalize(%q): %v", spelling, err)
		}
		if normalized != original {
			t.Fatalf("Normalize(%q) = %q, want %q", spelling, normalized, original)
		}
	}
}

func TestNormalizeRejectsWhatCannotBeACode(t *testing.T) {
	for _, spelling := range []string{
		"",
		"0123456789ABCDE",   // one short
		"0123456789ABCDEF0", // one long
		"0123456789ABCDEU",  // U is excluded from the alphabet
		"0123456789ABCDE!",  // punctuation
		"0123456789ABCDEФ",  // non-ASCII
	} {
		if _, err := Normalize(spelling); !errors.Is(err, ErrInvalidCode) {
			t.Fatalf("Normalize(%q) should have been rejected", spelling)
		}
	}
}

func TestHashIsStableAndOpaque(t *testing.T) {
	first, second := Hash("0123456789ABCDEF"), Hash("0123456789ABCDEF")
	if !bytes.Equal(first, second) {
		t.Fatal("hashing the same code twice must produce the same digest")
	}
	if len(first) != 32 {
		t.Fatalf("expected a 32-byte digest for the bytea constraint, got %d", len(first))
	}
	if bytes.Equal(first, Hash("0123456789ABCDEG")) {
		t.Fatal("distinct codes must not share a digest")
	}
}

func deliverable(now time.Time) State {
	return State{
		Status:        StatusDeliverable,
		ExpiresAt:     now.Add(24 * time.Hour),
		SenderUserID:  "sender",
		ClaimAttempts: 0,
	}
}

func TestEvaluateClaimAcceptsAnOrdinaryRedemption(t *testing.T) {
	now := time.Now().UTC()
	if rejection := EvaluateClaim(deliverable(now), Claimant{UserID: "recipient"}, now); rejection != RejectionNone {
		t.Fatalf("expected the claim to be accepted, got %q", rejection)
	}
}

func TestEvaluateClaimRefusesEveryDeadState(t *testing.T) {
	now := time.Now().UTC()
	claimant := Claimant{UserID: "recipient"}

	cases := map[string]Rejection{
		StatusPending:  RejectionNotSettled,
		StatusClaimed:  RejectionAlreadyClaimed,
		StatusRevoked:  RejectionRevoked,
		StatusRefunded: RejectionRevoked,
		StatusExpired:  RejectionExpired,
		"nonsense":     RejectionUnknownCode,
	}
	for status, want := range cases {
		state := deliverable(now)
		state.Status = status
		if got := EvaluateClaim(state, claimant, now); got != want {
			t.Fatalf("status %q: got %q, want %q", status, got, want)
		}
	}
}

func TestEvaluateClaimTreatsAnUnsweptExpiryAsExpired(t *testing.T) {
	now := time.Now().UTC()
	state := deliverable(now)
	// Still marked deliverable because the sweeper has not run, but over.
	state.ExpiresAt = now.Add(-time.Second)
	if got := EvaluateClaim(state, Claimant{UserID: "recipient"}, now); got != RejectionExpired {
		t.Fatalf("expected RejectionExpired, got %q", got)
	}
}

func TestEvaluateClaimRefusesTheSender(t *testing.T) {
	now := time.Now().UTC()
	if got := EvaluateClaim(deliverable(now), Claimant{UserID: "sender"}, now); got != RejectionSelfClaim {
		t.Fatalf("expected RejectionSelfClaim, got %q", got)
	}
}

func TestEvaluateClaimEnforcesANamedRecipient(t *testing.T) {
	now := time.Now().UTC()
	intended := int64(4242)
	state := deliverable(now)
	state.RecipientTelegramID = &intended

	if got := EvaluateClaim(state, Claimant{UserID: "someone"}, now); got != RejectionWrongRecipient {
		t.Fatalf("a claimant with no Telegram identity must not satisfy a named recipient, got %q", got)
	}

	other := int64(9999)
	if got := EvaluateClaim(state, Claimant{UserID: "someone", TelegramID: &other}, now); got != RejectionWrongRecipient {
		t.Fatalf("expected RejectionWrongRecipient, got %q", got)
	}
	if got := EvaluateClaim(state, Claimant{UserID: "someone", TelegramID: &intended}, now); got != RejectionNone {
		t.Fatalf("the named recipient must be accepted, got %q", got)
	}
}

func TestEvaluateClaimStopsAnsweringOnceTheAttemptCeilingIsReached(t *testing.T) {
	now := time.Now().UTC()
	state := deliverable(now)
	state.ClaimAttempts = MaxClaimAttempts

	// Even a claim that would otherwise succeed is refused, and refused with the
	// ceiling rather than with a reason that distinguishes the code's state.
	if got := EvaluateClaim(state, Claimant{UserID: "recipient"}, now); got != RejectionTooManyAttempts {
		t.Fatalf("expected RejectionTooManyAttempts, got %q", got)
	}
	state.Status = StatusClaimed
	if got := EvaluateClaim(state, Claimant{UserID: "recipient"}, now); got != RejectionTooManyAttempts {
		t.Fatalf("the ceiling must be checked before the status, got %q", got)
	}
}

func TestRevocationAndRefundEligibility(t *testing.T) {
	for status, revocable := range map[string]bool{
		StatusPending:     true,
		StatusDeliverable: true,
		StatusExpired:     true,
		StatusClaimed:     false,
		StatusRevoked:     false,
		StatusRefunded:    false,
	} {
		if CanRevoke(status) != revocable {
			t.Fatalf("CanRevoke(%q) = %v, want %v", status, CanRevoke(status), revocable)
		}
	}

	for status, refundable := range map[string]bool{
		StatusRevoked:     true,
		StatusExpired:     true,
		StatusClaimed:     false,
		StatusDeliverable: false,
		StatusPending:     false,
	} {
		if RefundEligible(status) != refundable {
			t.Fatalf("RefundEligible(%q) = %v, want %v", status, RefundEligible(status), refundable)
		}
	}
}
