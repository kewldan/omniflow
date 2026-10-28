//go:build integration

package integrationtest

import (
	"context"
	"slices"
	"testing"

	"github.com/omniflow/omniflow/internal/remnawave"
)

// TestBotDeviceRemovalActsOnTheChosenSubscription gives a customer two
// subscriptions, each with its own VPN user, and removes a device from the
// second one. The device screen of every subscription used to render the
// legacy buttons, whose handler resolved the customer-level Remnawave mapping
// — the first subscription — so the device was removed from the wrong user.
func TestBotDeviceRemovalActsOnTheChosenSubscription(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	const account = int64(704001)
	customerID := flow.customerOf(ctx, t, account)

	const firstUser, secondUser = int64(9101), int64(9102)
	if _, err := harness.pool.Exec(ctx, `INSERT INTO remnawave_users (user_id, remnawave_id, telegram_id)
		VALUES ($1::uuid, $2, $3)`, customerID, firstUser, account); err != nil {
		t.Fatalf("map the customer: %v", err)
	}
	var second string
	if _, err := harness.pool.Exec(ctx, `INSERT INTO subscriptions (user_id, slot, label, remnawave_user_id)
		VALUES ($1::uuid, 1, 'Phone', $2)`, customerID, firstUser); err != nil {
		t.Fatalf("create the first subscription: %v", err)
	}
	if err := harness.pool.QueryRow(ctx, `INSERT INTO subscriptions (user_id, slot, label, remnawave_user_id)
		VALUES ($1::uuid, 2, 'Laptop', $2) RETURNING id::text`, customerID, secondUser).Scan(&second); err != nil {
		t.Fatalf("create the second subscription: %v", err)
	}
	model := "MacBook"
	for _, user := range []int64{firstUser, secondUser} {
		flow.remnawave.users[user] = remnawave.User{ID: user, Status: "ACTIVE"}
		flow.remnawave.devices[user] = []remnawave.Device{
			{HWID: "hw-" + itoa64(user) + "-a", DeviceModel: &model},
			{HWID: "hw-" + itoa64(user) + "-b", DeviceModel: &model},
		}
	}

	flow.tap(ctx, account, 50, "act:sub-devices:"+second)
	screen := flow.telegram.lastScreen(t).callbacks(t)
	for _, want := range []string{"act:sub-dev-ask:" + second + ":1", "act:sub-devs-ask:" + second} {
		if !slices.Contains(screen, want) {
			t.Fatalf("the subscription's device screen lacks %q: %v", want, screen)
		}
	}
	flow.tap(ctx, account, 50, "act:sub-dev-ask:"+second+":1")
	if !slices.Contains(flow.telegram.lastScreen(t).callbacks(t), "act:sub-dev-del:"+second+":1") {
		t.Fatalf("the confirmation does not name the subscription: %v", flow.telegram.lastScreen(t).callbacks(t))
	}
	flow.tap(ctx, account, 50, "act:sub-dev-del:"+second+":1")
	flow.tap(ctx, account, 50, "act:sub-devs-del:"+second)

	want := []string{itoa64(secondUser) + "/hw-" + itoa64(secondUser) + "-b", itoa64(secondUser) + "/*"}
	if !slices.Equal(flow.remnawave.deleted, want) {
		t.Fatalf("removals = %v, want both on the second subscription's VPN user: %v", flow.remnawave.deleted, want)
	}
}
