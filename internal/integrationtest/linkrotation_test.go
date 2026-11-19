//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/remnawavesync"
)

type flakyRevoker struct {
	fail  bool
	calls int
}

func (revoker *flakyRevoker) RevokeSubscription(context.Context, int64) error {
	revoker.calls++
	if revoker.fail {
		return errors.New("panel down")
	}
	return nil
}

// A re-issue takes the subscription's slot for the cooldown; a second one
// inside it is refused without calling Remnawave, a failed one gives the slot
// back, and an operator's switch refuses all of them.
func TestLinkRotationCooldownIsSharedAndReleasedOnFailure(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	customer := harness.customer(ctx, t)
	subscription := harness.seedSubscription(ctx, t, customer, 1)
	var customerID, subscriptionID pgtype.UUID
	_ = customerID.Scan(customer)
	_ = subscriptionID.Scan(subscription)
	settings := commerce.ConnectSettings{RevokeCooldown: time.Hour}

	failing := &flakyRevoker{fail: true}
	if err := remnawavesync.RotateLink(ctx, harness.pool, failing, settings, customerID, subscriptionID, 7); err == nil {
		t.Fatal("a failed revoke was reported as done")
	}
	working := &flakyRevoker{}
	if err := remnawavesync.RotateLink(ctx, harness.pool, working, settings, customerID, subscriptionID, 7); err != nil {
		t.Fatalf("the failed attempt kept the slot: %v", err)
	}
	err := remnawavesync.RotateLink(ctx, harness.pool, working, settings, customerID, subscriptionID, 7)
	var cooldown *remnawavesync.RotationCooldownError
	if !errors.As(err, &cooldown) || time.Until(cooldown.RetryAt) < 55*time.Minute || working.calls != 1 {
		t.Fatalf("second re-issue: %v (calls %d)", err, working.calls)
	}
	stranger := harness.customer(ctx, t)
	var strangerID pgtype.UUID
	_ = strangerID.Scan(stranger)
	if err := remnawavesync.RotateLink(ctx, harness.pool, working, commerce.ConnectSettings{}, strangerID, subscriptionID, 7); !errors.Is(err, remnawavesync.ErrRotationUnknown) {
		t.Fatalf("another customer's subscription: %v", err)
	}
	if err := remnawavesync.RotateLink(ctx, harness.pool, working, commerce.ConnectSettings{RevokeDisabled: true}, customerID, subscriptionID, 7); !errors.Is(err, remnawavesync.ErrRotationDisabled) {
		t.Fatalf("switched off: %v", err)
	}
}
