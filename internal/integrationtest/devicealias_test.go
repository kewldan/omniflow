//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/omniflow/omniflow/internal/accountpg"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// A customer names a device that is on their subscription now; the name comes
// back with the device list, an empty name clears it, and a handle that names
// no current device is refused.
func TestDeviceAliasesFollowTheDeviceList(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	customer := harness.customer(ctx, t)
	subscription := harness.seedSubscription(ctx, t, customer, 1)
	harness.seedSubscriptionEntitlement(ctx, t, customer, subscription, harness.catalog(ctx, t, "alias-plan", 10000), "active", "30 days")
	if _, err := harness.pool.Exec(ctx, `UPDATE subscriptions SET remnawave_user_id = 4242 WHERE id = $1::uuid`, subscription); err != nil {
		t.Fatal(err)
	}
	model := "Pixel 9"
	fake := &botRemnawave{
		users:   map[int64]remnawave.User{4242: {ID: 4242, Status: "ACTIVE"}},
		devices: map[int64][]remnawave.Device{4242: {{HWID: "hw-1", DeviceModel: &model}}},
	}
	account, err := accountpg.New(harness.pool, fake, accountpg.Options{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	handle := commerce.DeviceHandle("hw-1")
	if err := account.RenameDevice(ctx, customer, subscription, handle, "  Work   phone "); err != nil {
		t.Fatal(err)
	}
	devices, err := account.Devices(ctx, customer, subscription)
	if err != nil || len(devices) != 1 || devices[0].Alias != "Work phone" || devices[0].Name != "Pixel 9" {
		t.Fatalf("devices = %+v (%v)", devices, err)
	}
	if err := account.RenameDevice(ctx, customer, subscription, commerce.DeviceHandle("gone"), "x"); !errors.Is(err, accountpg.ErrNotFound) {
		t.Fatalf("a device that is not there: %v", err)
	}
	if err := account.RenameDevice(ctx, customer, subscription, handle, ""); err != nil {
		t.Fatal(err)
	}
	if devices, _ = account.Devices(ctx, customer, subscription); devices[0].Alias != "" {
		t.Fatalf("an empty name must clear it: %+v", devices)
	}
}
