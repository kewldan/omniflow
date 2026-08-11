package adminauth

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestNewSessionTokenReturnsMatchingDigest(t *testing.T) {
	token, digest, err := NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}
	if token == "" {
		t.Fatal("token is empty")
	}
	if len(digest) != 32 {
		t.Fatalf("digest is %d bytes, want 32", len(digest))
	}
	if !bytes.Equal(digest, HashSessionToken(token)) {
		t.Fatal("digest does not match HashSessionToken of the token")
	}
}

func TestNewSessionTokenIsUnique(t *testing.T) {
	seen := make(map[string]struct{}, 64)
	for range 64 {
		token, _, err := NewSessionToken()
		if err != nil {
			t.Fatalf("NewSessionToken: %v", err)
		}
		if _, duplicate := seen[token]; duplicate {
			t.Fatal("NewSessionToken repeated a token")
		}
		seen[token] = struct{}{}
	}
}

// A CSRF token is bound to its session secret, so one minted for session A must
// not validate against session B.
func TestCSRFTokenIsBoundToItsSession(t *testing.T) {
	first, err := NewCSRFSecret()
	if err != nil {
		t.Fatalf("NewCSRFSecret: %v", err)
	}
	second, err := NewCSRFSecret()
	if err != nil {
		t.Fatalf("NewCSRFSecret: %v", err)
	}

	token := CSRFToken(first)
	if !ValidCSRFToken(first, token) {
		t.Fatal("a token did not validate against its own secret")
	}
	if ValidCSRFToken(second, token) {
		t.Fatal("a token validated against a different session's secret")
	}
	if ValidCSRFToken(first, "") {
		t.Fatal("an empty token was accepted")
	}
	if ValidCSRFToken(nil, token) {
		t.Fatal("a missing secret accepted a token")
	}
}

func TestSessionDeadlinesUseTheChallengeWindowWhilePending(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()

	idle, absolute := DefaultSessionPolicy.SessionDeadlines(now, false)
	if want := now.Add(DefaultSessionPolicy.IdleTimeout); !idle.Equal(want) {
		t.Fatalf("idle = %s, want %s", idle, want)
	}
	if want := now.Add(DefaultSessionPolicy.AbsoluteTimeout); !absolute.Equal(want) {
		t.Fatalf("absolute = %s, want %s", absolute, want)
	}

	pendingIdle, _ := DefaultSessionPolicy.SessionDeadlines(now, true)
	if want := now.Add(DefaultSessionPolicy.ChallengeTimeout); !pendingIdle.Equal(want) {
		t.Fatalf("pending idle = %s, want %s", pendingIdle, want)
	}
}

// Sliding the idle window must never push a session past its absolute horizon.
func TestNextIdleDeadlineIsClampedToTheAbsoluteDeadline(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	absolute := now.Add(5 * time.Minute)

	deadline := DefaultSessionPolicy.NextIdleDeadline(now, absolute)
	if !deadline.Equal(absolute) {
		t.Fatalf("deadline = %s, want it clamped to %s", deadline, absolute)
	}

	far := now.Add(24 * time.Hour)
	deadline = DefaultSessionPolicy.NextIdleDeadline(now, far)
	if want := now.Add(DefaultSessionPolicy.IdleTimeout); !deadline.Equal(want) {
		t.Fatalf("deadline = %s, want %s", deadline, want)
	}
}

func TestValidateRejectsExpiredAndRevokedSessions(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	live := SessionState{
		RotatedAt:       now,
		IdleExpiresAt:   now.Add(time.Minute),
		AbsoluteExpires: now.Add(time.Hour),
	}
	if err := DefaultSessionPolicy.Validate(live, now); err != nil {
		t.Fatalf("a live session was rejected: %v", err)
	}

	idleExpired := live
	idleExpired.IdleExpiresAt = now.Add(-time.Second)
	if err := DefaultSessionPolicy.Validate(idleExpired, now); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expected ErrSessionExpired for an idle-expired session, got %v", err)
	}

	absoluteExpired := live
	absoluteExpired.AbsoluteExpires = now.Add(-time.Second)
	if err := DefaultSessionPolicy.Validate(absoluteExpired, now); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expected ErrSessionExpired for an absolutely expired session, got %v", err)
	}

	revokedAt := now.Add(-time.Minute)
	revoked := live
	revoked.RevokedAt = &revokedAt
	if err := DefaultSessionPolicy.Validate(revoked, now); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("expected ErrSessionRevoked, got %v", err)
	}
}

func TestShouldRotateAfterTheRotationWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	state := SessionState{RotatedAt: now.Add(-DefaultSessionPolicy.RotateAfter)}
	if !DefaultSessionPolicy.ShouldRotate(state, now) {
		t.Fatal("a session at the rotation window was not rotated")
	}

	fresh := SessionState{RotatedAt: now.Add(-time.Second)}
	if DefaultSessionPolicy.ShouldRotate(fresh, now) {
		t.Fatal("a freshly rotated session was rotated again")
	}

	disabled := SessionPolicy{RotateAfter: 0}
	if disabled.ShouldRotate(state, now) {
		t.Fatal("rotation ran with RotateAfter disabled")
	}
}

func TestLockoutBacksOffExponentiallyAndCaps(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	policy := DefaultLockoutPolicy

	for failures := range policy.Threshold {
		if until := policy.LockoutUntil(failures, now); until != nil {
			t.Fatalf("%d failures locked the account before the threshold", failures)
		}
	}

	cases := []struct {
		failures int
		delay    time.Duration
	}{
		{policy.Threshold, policy.BaseDelay},
		{policy.Threshold + 1, 2 * policy.BaseDelay},
		{policy.Threshold + 2, 4 * policy.BaseDelay},
	}
	for _, testCase := range cases {
		until := policy.LockoutUntil(testCase.failures, now)
		if until == nil {
			t.Fatalf("%d failures did not lock the account", testCase.failures)
		}
		if want := now.Add(testCase.delay); !until.Equal(want) {
			t.Fatalf("%d failures: locked until %s, want %s", testCase.failures, until, want)
		}
	}

	// A very large failure count must saturate at the cap rather than overflow.
	until := policy.LockoutUntil(policy.Threshold+1000, now)
	if until == nil {
		t.Fatal("a large failure count did not lock the account")
	}
	if want := now.Add(policy.MaxDelay); !until.Equal(want) {
		t.Fatalf("locked until %s, want the cap %s", until, want)
	}
}

func TestLocked(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	future := now.Add(time.Minute)
	past := now.Add(-time.Minute)

	if Locked(nil, now) {
		t.Fatal("a nil deadline reported locked")
	}
	if !Locked(&future, now) {
		t.Fatal("a future deadline reported unlocked")
	}
	if Locked(&past, now) {
		t.Fatal("an elapsed deadline reported locked")
	}
}
