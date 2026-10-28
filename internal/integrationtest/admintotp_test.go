//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/adminauth"
	"github.com/omniflow/omniflow/internal/adminauthpg"
)

// A TOTP code is accepted once, and guessing one is bounded per pending session
// and per account — the per-address rate limit alone let a caller rotating
// addresses grind through the million codes, and an accepted code could be
// replayed for the rest of its window.
func TestAdminTOTPIsSingleUseAndGuessingIsBounded(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	now := time.Now().UTC()
	service := newAdminService(t, harness, func() time.Time { return now })
	owner := bootstrapOwner(t, ctx, service)
	request := adminauthpg.RequestContext{}
	const password = "correct horse battery staple"

	enrolment, err := service.BeginTOTPEnrolment(ctx, owner.ID, "Omniflow", request)
	if err != nil {
		t.Fatalf("begin enrolment: %v", err)
	}
	codeAt := func(at time.Time) string {
		code, codeErr := adminauth.TOTPCode(enrolment.Secret, uint64(at.Unix()/30))
		if codeErr != nil {
			t.Fatalf("derive code: %v", codeErr)
		}
		return code
	}
	wrongCode := func() string {
		for _, candidate := range []string{"000000", "111111", "222222"} {
			if candidate != codeAt(now) && candidate != codeAt(now.Add(-30*time.Second)) &&
				candidate != codeAt(now.Add(30*time.Second)) {
				return candidate
			}
		}
		t.Fatal("no wrong code available")
		return ""
	}
	pending := func() string {
		result, authErr := service.Authenticate(ctx, "owner@example.com", password, request)
		if authErr != nil || !result.ChallengeRequired {
			t.Fatalf("authenticate: %+v %v", result, authErr)
		}
		return result.Token
	}

	enrolCode := codeAt(now)
	if _, err = service.ConfirmTOTPEnrolment(ctx, owner.ID, enrolCode, request); err != nil {
		t.Fatalf("confirm enrolment: %v", err)
	}

	// The code that confirmed enrolment is spent.
	if _, err = service.VerifyChallenge(ctx, pending(), enrolCode, request); !errors.Is(err, adminauthpg.ErrInvalidCredentials) {
		t.Fatalf("the enrolment code was replayed into a sign-in: %v", err)
	}

	// The next step's code works once, and only once.
	now = now.Add(30 * time.Second)
	fresh := codeAt(now)
	if _, err = service.VerifyChallenge(ctx, pending(), fresh, request); err != nil {
		t.Fatalf("a fresh code was refused: %v", err)
	}
	session := pending()
	if _, err = service.VerifyChallenge(ctx, session, fresh, request); !errors.Is(err, adminauthpg.ErrInvalidCredentials) {
		t.Fatalf("an accepted code was replayed: %v", err)
	}

	// The same pending session now holds one failure. Three more are answered
	// as wrong codes; the fifth revokes the session.
	for attempt := 2; attempt <= 4; attempt++ {
		if _, err = service.VerifyChallenge(ctx, session, wrongCode(), request); !errors.Is(err, adminauthpg.ErrInvalidCredentials) {
			t.Fatalf("wrong code %d: %v", attempt, err)
		}
	}
	if _, err = service.VerifyChallenge(ctx, session, wrongCode(), request); !errors.Is(err, adminauthpg.ErrSessionInvalid) {
		t.Fatalf("the fifth wrong code did not end the session: %v", err)
	}
	var reason string
	if err = harness.pool.QueryRow(ctx,
		`SELECT revoked_reason FROM admin_sessions WHERE challenge_failures = $1`, adminauthpg.MaxChallengeFailures,
	).Scan(&reason); err != nil || reason != "challenge_failed" {
		t.Fatalf("revoked_reason = %q, %v", reason, err)
	}
	if _, err = service.VerifyChallenge(ctx, session, codeAt(now), request); !errors.Is(err, adminauthpg.ErrSessionInvalid) {
		t.Fatalf("a revoked pending session accepted a code: %v", err)
	}

	// Five wrong codes across sessions locked the account, the password step
	// included.
	if _, err = service.Authenticate(ctx, "owner@example.com", password, request); !errors.Is(err, adminauthpg.ErrAccountLocked) {
		t.Fatalf("authenticate after five wrong codes: %v", err)
	}

	// After the lock lapses a correct password does not reset the count: the
	// very next wrong code locks again, and the lock holds the challenge too.
	now = now.Add(2 * time.Minute)
	session = pending()
	if _, err = service.VerifyChallenge(ctx, session, wrongCode(), request); !errors.Is(err, adminauthpg.ErrInvalidCredentials) {
		t.Fatalf("wrong code after the lock: %v", err)
	}
	if _, err = service.VerifyChallenge(ctx, session, codeAt(now), request); !errors.Is(err, adminauthpg.ErrAccountLocked) {
		t.Fatalf("a correct code during the renewed lock: %v", err)
	}
}
