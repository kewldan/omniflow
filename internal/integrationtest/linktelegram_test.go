//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/customerauth"
	"github.com/omniflow/omniflow/internal/customerauthpg"
)

// A customer who signed in another way attaches their Telegram account from the
// panel. Linking twice is harmless, a Telegram account that already belongs to
// somebody else is refused rather than moved, and one customer holds one
// Telegram account.
func TestLinkTelegramToAnExistingCustomer(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	now := time.Now().UTC()
	service := newCustomerService(t, harness, func() time.Time { return now })

	var customer string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO users (locale) VALUES ('ru') RETURNING id::text`).Scan(&customer); err != nil {
		t.Fatalf("create customer: %v", err)
	}

	if err := service.LinkTelegramIdentity(ctx, customer, signedWidget(710001, now), customerauthpg.RequestContext{}); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := service.LinkTelegramIdentity(ctx, customer, signedWidget(710001, now), customerauthpg.RequestContext{}); err != nil {
		t.Fatalf("linking the same account again should be a no-op: %v", err)
	}

	// Signing in with that Telegram account now lands on the same customer
	// instead of creating a second one.
	signedIn, err := service.SignInWithTelegram(ctx, signedWidget(710001, now), customerauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	if signedIn.Customer.ID != customer {
		t.Fatalf("telegram sign-in reached %s, want the linked customer %s", signedIn.Customer.ID, customer)
	}

	// A second Telegram account on the same customer is refused.
	err = service.LinkTelegramIdentity(ctx, customer, signedWidget(710002, now), customerauthpg.RequestContext{})
	if !errors.Is(err, customerauthpg.ErrTelegramAlreadyLinked) {
		t.Fatalf("second telegram account: got %v, want ErrTelegramAlreadyLinked", err)
	}

	// A Telegram account somebody else already uses is never moved.
	other, err := service.SignInWithTelegram(ctx, signedWidget(710003, now), customerauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("other customer: %v", err)
	}
	var stranger string
	if err = harness.pool.QueryRow(ctx, `INSERT INTO users (locale) VALUES ('en') RETURNING id::text`).Scan(&stranger); err != nil {
		t.Fatalf("create stranger: %v", err)
	}
	err = service.LinkTelegramIdentity(ctx, stranger, signedWidget(710003, now), customerauthpg.RequestContext{})
	if !errors.Is(err, customerauth.ErrIdentityTaken) {
		t.Fatalf("taken account: got %v, want ErrIdentityTaken", err)
	}
	var owner string
	if err = harness.pool.QueryRow(ctx,
		`SELECT user_id::text FROM identities WHERE provider = 'telegram' AND provider_subject = '710003'`,
	).Scan(&owner); err != nil || owner != other.Customer.ID {
		t.Fatalf("the taken identity moved to %s (err %v), want it to stay with %s", owner, err, other.Customer.ID)
	}

	// A forged payload links nothing.
	forged := signedWidget(710004, now)
	forged.Set("hash", "00")
	if err = service.LinkTelegramIdentity(ctx, stranger, forged, customerauthpg.RequestContext{}); !errors.Is(err, customerauthpg.ErrSignInRejected) {
		t.Fatalf("forged payload: got %v, want ErrSignInRejected", err)
	}

	var events int
	if err = harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM customer_security_events WHERE user_id = $1 AND event = 'identity_linked'`, customer,
	).Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if events != 1 {
		t.Fatalf("identity_linked events = %d, want 1 (the repeat link is a no-op)", events)
	}
}
