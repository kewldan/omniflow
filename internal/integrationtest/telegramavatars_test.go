//go:build integration

package integrationtest

import (
	"context"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/telegramavatar"
)

// TestTelegramAvatarsAreStoredAndErasedWithTheCustomer: the stored photo
// round-trips through the store, and anonymising its customer erases it while
// leaving another customer's photo alone.
func TestTelegramAvatarsAreStoredAndErasedWithTheCustomer(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	customer := harness.namedTelegramCustomer(ctx, t, store, "7500001", "avatar_owner")
	harness.namedTelegramCustomer(ctx, t, store, "7500002", "avatar_neighbour")

	avatars := telegramavatar.NewPostgresStore(harness.pool)
	if err := avatars.Save(ctx, 7500001, []byte("\xff\xd8owner")); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := avatars.Save(ctx, 7500002, []byte("\xff\xd8neighbour")); err != nil {
		t.Fatalf("save: %v", err)
	}
	photo, fetchedAt, found, err := avatars.Load(ctx, 7500001)
	if err != nil || !found || string(photo) != "\xff\xd8owner" || time.Since(fetchedAt) > time.Minute {
		t.Fatalf("load: %q %v %v %v", photo, fetchedAt, found, err)
	}
	if err = avatars.Save(ctx, 7500003, nil); err != nil {
		t.Fatalf("save no photo: %v", err)
	}
	if photo, _, found, _ = avatars.Load(ctx, 7500003); !found || photo != nil {
		t.Fatalf("a recorded missing photo loads as %q, found %v", photo, found)
	}

	if err = dbgen.New(harness.pool).AnonymizeCustomerData(ctx, mustUUID(t, customer)); err != nil {
		t.Fatalf("anonymise: %v", err)
	}
	if _, _, found, _ = avatars.Load(ctx, 7500001); found {
		t.Fatal("the anonymised customer's photo is still stored")
	}
	if _, _, found, _ = avatars.Load(ctx, 7500002); !found {
		t.Fatal("anonymising one customer erased another's photo")
	}
}
