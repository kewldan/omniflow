package adminauth

import (
	"testing"
	"time"
)

// The replay guard stores the step a code matched, so MatchTOTP must report the
// step the code was generated for — including a code from the step before or
// after, which drift tolerance accepts.
func TestMatchTOTPReportsTheStepTheCodeBelongsTo(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("secret: %v", err)
	}
	now := time.Unix(1_800_000_000, 0)
	current := uint64(now.Unix()) / 30
	for _, step := range []uint64{current - 1, current, current + 1} {
		code, codeErr := TOTPCode(secret, step)
		if codeErr != nil {
			t.Fatalf("code: %v", codeErr)
		}
		matched, ok, matchErr := MatchTOTP(secret, code, now)
		if matchErr != nil || !ok {
			t.Fatalf("step %d: ok=%v err=%v", step, ok, matchErr)
		}
		// A code can collide with another step's; the newest wins, which is
		// never older than the step it was generated for.
		if matched < step {
			t.Fatalf("step %d reported as %d", step, matched)
		}
	}
	if _, ok, _ := MatchTOTP(secret, "12345", now); ok {
		t.Fatal("a five-digit code matched")
	}
}
