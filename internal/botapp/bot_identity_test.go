package botapp

import (
	"context"
	"errors"
	"testing"
	"time"
)

// getMe failing at startup used to leave referral links off until a restart.
// The username is now looked up again when a screen needs it — not on every
// screen, but once per backoff interval, which doubles up to an hour.
func TestBotIdentityIsLookedUpAgainWithBackoff(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	calls, answer := 0, ""
	identity := &botIdentity{clock: func() time.Time { return now }}
	identity.useLookup(func(context.Context) (string, error) {
		calls++
		if answer == "" {
			return "", errors.New("telegram unreachable")
		}
		return answer, nil
	})
	ctx := context.Background()

	if identity.get(ctx) != "" || calls != 1 {
		t.Fatalf("the first lookup did not happen: %d calls", calls)
	}
	if identity.get(ctx) != "" || calls != 1 {
		t.Fatalf("a lookup inside the backoff reached Telegram: %d calls", calls)
	}
	now = now.Add(identityRetryFirst)
	identity.get(ctx)
	if calls != 2 {
		t.Fatalf("the lookup was not retried after the backoff: %d calls", calls)
	}
	now = now.Add(identityRetryFirst)
	identity.get(ctx)
	if calls != 2 {
		t.Fatal("the backoff did not grow after a second failure")
	}

	answer = "@omniflow_bot"
	now = now.Add(2 * identityRetryFirst)
	if got := identity.get(ctx); got != "omniflow_bot" {
		t.Fatalf("the username after Telegram answered is %q", got)
	}
	if identity.get(ctx) != "omniflow_bot" || calls != 3 {
		t.Fatalf("a known username was looked up again: %d calls", calls)
	}
}

// A username supplied at startup is used as it is, and no lookup is made.
func TestBotIdentityKnownAtStartupIsNotLookedUp(t *testing.T) {
	t.Parallel()
	identity := &botIdentity{}
	identity.set(" @omniflow_bot ")
	identity.useLookup(func(context.Context) (string, error) {
		t.Fatal("a known username was looked up")
		return "", nil
	})
	if got := identity.get(context.Background()); got != "omniflow_bot" {
		t.Fatalf("username = %q", got)
	}
}
