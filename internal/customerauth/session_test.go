package customerauth

import (
	"errors"
	"testing"
	"time"
)

func liveSession(now time.Time) SessionState {
	return SessionState{
		CreatedAt:       now,
		RotatedAt:       now,
		IdleExpiresAt:   now.Add(DefaultSessionPolicy.IdleTimeout),
		AbsoluteExpires: now.Add(DefaultSessionPolicy.AbsoluteTimeout),
	}
}

func TestValidateRejectsExpiredAndRevokedSessions(t *testing.T) {
	now := time.Now().UTC()
	policy := DefaultSessionPolicy

	if err := policy.Validate(liveSession(now), now); err != nil {
		t.Fatalf("a live session was refused: %v", err)
	}

	idle := liveSession(now)
	idle.IdleExpiresAt = now.Add(-time.Minute)
	if err := policy.Validate(idle, now); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("idle expiry: %v, want ErrSessionExpired", err)
	}

	absolute := liveSession(now)
	absolute.AbsoluteExpires = now.Add(-time.Minute)
	if err := policy.Validate(absolute, now); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("absolute expiry: %v, want ErrSessionExpired", err)
	}

	// Revocation is reported ahead of expiry so the more specific reason survives
	// when both apply.
	revoked := absolute
	revokedAt := now.Add(-time.Hour)
	revoked.RevokedAt = &revokedAt
	if err := policy.Validate(revoked, now); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("revoked: %v, want ErrSessionRevoked", err)
	}
}

func TestNextIdleDeadlineIsClampedToTheAbsoluteDeadline(t *testing.T) {
	now := time.Now().UTC()
	policy := DefaultSessionPolicy
	absolute := now.Add(time.Hour)

	// Without the clamp, a request made just before the absolute horizon would
	// push the idle window past it and keep alive a session the absolute limit
	// had already ended.
	if got := policy.NextIdleDeadline(now, absolute); !got.Equal(absolute) {
		t.Fatalf("deadline = %v, want the absolute deadline %v", got, absolute)
	}

	far := now.Add(365 * 24 * time.Hour)
	if got := policy.NextIdleDeadline(now, far); !got.Equal(now.Add(policy.IdleTimeout)) {
		t.Fatalf("deadline = %v, want now + idle timeout", got)
	}
}

func TestShouldRotateAfterTheRotationWindow(t *testing.T) {
	now := time.Now().UTC()
	policy := DefaultSessionPolicy

	fresh := liveSession(now)
	if policy.ShouldRotate(fresh, now) {
		t.Fatal("a session rotated a moment ago is due for rotation again")
	}

	stale := liveSession(now)
	stale.RotatedAt = now.Add(-policy.RotateAfter - time.Minute)
	if !policy.ShouldRotate(stale, now) {
		t.Fatal("a session past the rotation window was not rotated")
	}

	off := policy
	off.RotateAfter = 0
	if off.ShouldRotate(stale, now) {
		t.Fatal("rotation happened with the window disabled")
	}
}

func TestRequiresReauthenticationMeasuresFromTheSignIn(t *testing.T) {
	now := time.Now().UTC()
	policy := DefaultSessionPolicy

	fresh := liveSession(now)
	if policy.RequiresReauthentication(fresh, now) {
		t.Fatal("a session that just signed in was asked to re-authenticate")
	}

	// The window is measured from CreatedAt rather than from activity: a session
	// kept busy all day is not freshly authenticated, and treating it as such
	// would let a browser left open on a shared machine rotate somebody's key.
	old := liveSession(now.Add(-time.Hour))
	old.IdleExpiresAt = now.Add(policy.IdleTimeout)
	if !policy.RequiresReauthentication(old, now) {
		t.Fatal("an hour-old session was allowed a destructive action")
	}

	off := policy
	off.ReauthWindow = 0
	if off.RequiresReauthentication(old, now) {
		t.Fatal("re-authentication was demanded with the window disabled")
	}
}

func TestCustomerSessionsOutliveOperatorSessions(t *testing.T) {
	// A customer checking their remaining days is not doing what an operator
	// moving money is doing, and a panel that logs them out over lunch pushes
	// them back to the bot. The relationship is asserted so a future tweak to
	// either policy has to be deliberate.
	if DefaultSessionPolicy.IdleTimeout <= time.Hour {
		t.Fatalf("customer idle timeout = %v, unexpectedly short", DefaultSessionPolicy.IdleTimeout)
	}
	if DefaultSessionPolicy.AbsoluteTimeout <= DefaultSessionPolicy.IdleTimeout {
		t.Fatal("the absolute timeout must exceed the idle timeout")
	}
	if DefaultSessionPolicy.ReauthWindow >= DefaultSessionPolicy.IdleTimeout {
		t.Fatal("the re-authentication window must be much shorter than the session")
	}
}

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
		t.Fatal("a token did not validate against its own session secret")
	}
	if ValidCSRFToken(second, token) {
		t.Fatal("a token minted for one session validated inside another")
	}
	if ValidCSRFToken(first, "") || ValidCSRFToken(nil, token) {
		t.Fatal("an empty token or secret was accepted")
	}
}

func TestSessionTokensAreUniqueAndOnlyStoredAsDigests(t *testing.T) {
	seen := make(map[string]struct{}, 64)
	for range 64 {
		token, digest, err := NewSessionToken()
		if err != nil {
			t.Fatalf("NewSessionToken: %v", err)
		}
		if _, repeated := seen[token]; repeated {
			t.Fatal("NewSessionToken repeated a token")
		}
		seen[token] = struct{}{}
		if len(digest) != 32 {
			t.Fatalf("digest length = %d, want 32", len(digest))
		}
		if string(digest) == token {
			t.Fatal("the stored digest is the token itself")
		}
	}
}
