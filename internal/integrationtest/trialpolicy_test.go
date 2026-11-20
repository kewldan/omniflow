//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/panelpg"
)

// With Telegram excluded from trials, a Telegram-only customer is refused one
// on both surfaces' trial query, and a customer who also has an e-mail contact
// still qualifies.
func TestATrialIsRefusedByTheWayTheCustomerSignsIn(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	checkout := newAccountCheckout(t, harness, store)
	harness.savePolicy(ctx, t, func(policy *panelpg.SubscriptionPolicy) {
		policy.TrialDisabledMethods = []string{"telegram"}
		policy.TrialWarningHours = 6
	})

	telegramOnly := harness.customer(ctx, t)
	both := harness.customer(ctx, t)
	for index, customer := range []string{telegramOnly, both} {
		if _, err := harness.pool.Exec(ctx, `INSERT INTO identities (user_id, provider, provider_subject, status)
			VALUES ($1::uuid, 'telegram', $2, 'active')`, customer, []string{"9001", "9002"}[index]); err != nil {
			t.Fatalf("link Telegram: %v", err)
		}
	}
	if _, err := harness.pool.Exec(ctx, `INSERT INTO contact_channels (user_id, kind, value_fingerprint)
		VALUES ($1::uuid, 'email', decode(repeat('ab', 32), 'hex'))`, both); err != nil {
		t.Fatalf("add an e-mail contact: %v", err)
	}

	refused, err := checkout.Store().TrialContext(ctx, telegramOnly)
	if err != nil {
		t.Fatalf("trial context: %v", err)
	}
	if !refused.SignInMethodExcluded {
		t.Fatal("a Telegram-only customer qualified with Telegram excluded")
	}
	allowed, err := checkout.Store().TrialContext(ctx, both)
	if err != nil {
		t.Fatalf("trial context: %v", err)
	}
	if allowed.SignInMethodExcluded {
		t.Fatal("a customer who also signs in by e-mail was refused")
	}
}
