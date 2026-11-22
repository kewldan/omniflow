//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/adminauthpg"
)

// TestPasswordSignInCanBeTurnedOffAndReopened walks the passkeys-only switch:
// refused before a passkey has worked; once off, a correct password is told
// it is off and a wrong one gets the usual answer; the last passkey cannot be
// removed; and the server's command line reopens password sign-in for a while
// or for good.
func TestPasswordSignInCanBeTurnedOffAndReopened(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := newPasskeyService(t, harness)
	owner := bootstrapOwner(t, ctx, service)
	const password = "correct horse battery staple"
	request := adminauthpg.RequestContext{RequestID: "password-switch"}

	device := newAuthenticator(t)
	key, err := registerPasskey(t, ctx, service, device, owner.ID, "Laptop")
	if err != nil {
		t.Fatalf("register passkey: %v", err)
	}
	if _, err = service.SetPasswordLogin(ctx, owner.ID, false, request); !errors.Is(err, adminauthpg.ErrNoWorkingPasskey) {
		t.Fatalf("turning passwords off before a passkey worked gave %v", err)
	}
	device.signCount++
	if _, err = signIn(t, ctx, service, device, accountHandle(t, owner.ID)); err != nil {
		t.Fatalf("passkey sign-in: %v", err)
	}
	account, err := service.SetPasswordLogin(ctx, owner.ID, false, request)
	if err != nil || !account.PasswordLoginDisabled {
		t.Fatalf("turning passwords off: %v, disabled %v", err, account.PasswordLoginDisabled)
	}

	if _, err = service.Authenticate(ctx, "owner@example.com", password, request); !errors.Is(err, adminauthpg.ErrPasswordLoginDisabled) {
		t.Fatalf("a correct password with passwords off gave %v", err)
	}
	if _, err = service.Authenticate(ctx, "owner@example.com", "wrong password entirely", request); !errors.Is(err, adminauthpg.ErrInvalidCredentials) {
		t.Fatalf("a wrong password with passwords off gave %v, want the usual refusal", err)
	}
	if err = service.DeletePasskey(ctx, owner.ID, key.ID, request); !errors.Is(err, adminauthpg.ErrLastPasskey) {
		t.Fatalf("removing the last passkey gave %v", err)
	}

	// The command line's temporary reopening lets the password in.
	if _, err = service.ReopenPasswordLogin(ctx, "owner@example.com", time.Hour); err != nil {
		t.Fatalf("reopen for an hour: %v", err)
	}
	if _, err = service.Authenticate(ctx, "owner@example.com", password, request); errors.Is(err, adminauthpg.ErrPasswordLoginDisabled) || errors.Is(err, adminauthpg.ErrInvalidCredentials) {
		t.Fatalf("a correct password during a reopening gave %v", err)
	}
	// For good: the switch itself is back on.
	account, err = service.ReopenPasswordLogin(ctx, "owner@example.com", 0)
	if err != nil || account.PasswordLoginDisabled {
		t.Fatalf("reopen for good: %v, disabled %v", err, account.PasswordLoginDisabled)
	}
	if err = service.DeletePasskey(ctx, owner.ID, key.ID, request); err != nil {
		t.Fatalf("removing the passkey once passwords work again: %v", err)
	}
}
