//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/remnawave"
	"github.com/omniflow/omniflow/internal/remnawavesync"
)

// A second account on the same phone: the scan finds it, the trial it would
// take is refused, the referral it would earn is held, and the operator's
// verdict lifts both until another account joins the device.
func TestSharedDeviceScanFindsSecondAccounts(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)
	logger := slog.New(slog.DiscardHandler)
	now := time.Now().UTC().Truncate(time.Second)

	owner := harness.customer(ctx, t)
	second := harness.customer(ctx, t)
	operatorCustomer := harness.customer(ctx, t)
	harness.exec(ctx, t, `INSERT INTO subscriptions (user_id, slot, label, remnawave_user_id) VALUES
		($1::uuid, 1, 'Phone', 101), ($2::uuid, 1, 'Phone', 102), ($3::uuid, 1, 'Phone', 103)`,
		owner, second, operatorCustomer)
	harness.exec(ctx, t, `INSERT INTO identities (user_id, provider, provider_subject, verified_at, status)
		VALUES ($1::uuid, 'telegram', '777', now(), 'active')`, operatorCustomer)

	// The owner took the trial and then invited the second account.
	order := seedPaidOrder(ctx, t, harness, owner, "shared-device-trial")
	planID := harness.planIDOf(ctx, t, harness.catalog(ctx, t, "shared-device-trial", 10000))
	harness.exec(ctx, t, `INSERT INTO trial_claims (user_id, plan_id, order_id) VALUES ($1::uuid, $2::uuid, $3::uuid)`,
		owner, planID, order)
	harness.exec(ctx, t, `INSERT INTO referral_codes (user_id, code) VALUES ($1::uuid, 'OWNER00001')`, owner)
	harness.exec(ctx, t, `INSERT INTO referral_attributions (code, referrer_user_id, referred_user_id)
		VALUES ('OWNER00001', $1::uuid, $2::uuid)`, owner, second)

	platform := "iOS"
	panel := &monitorPanel{devices: []remnawave.PanelDevice{
		{UserID: 101, Handle: "phoneHandle01", Platform: &platform, UpdatedAt: now.Add(-time.Hour)},
		{UserID: 101, Handle: "tabletHandle1", UpdatedAt: now.Add(-time.Hour)},
		{UserID: 102, Handle: "phoneHandle01", Platform: &platform, UpdatedAt: now},
		// The operator testing on the same phone is not a second account.
		{UserID: 103, Handle: "phoneHandle01", UpdatedAt: now},
		// A device with no handle (no HWID reported) is skipped.
		{UserID: 102, UpdatedAt: now},
	}}
	scanner := remnawavesync.NewDeviceScanner(harness.pool, panel, []int64{777}, logger)

	result, err := scanner.Scan(ctx, now)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if result.NewlyOpen != 1 || result.ReferralsHeld != 1 || result.Sightings != 3 {
		t.Fatalf("result = %+v", result)
	}
	page, err := operations.SearchSharedDevices(ctx, panelpg.SharedDeviceFilter{Status: "open"})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("queue = %+v (%v)", page, err)
	}
	device := page.Items[0]
	if device.Handle != "phoneHandle01" || device.Accounts != 2 || device.TrialAccounts != 1 ||
		device.ReferralPairs != 1 || device.Platform != "iOS" || len(device.Customers) != 2 {
		t.Fatalf("device = %+v", device)
	}
	for _, account := range device.Customers {
		if account.CustomerID == second && account.InvitedBy != owner {
			t.Fatalf("second account not shown as invited by the owner: %+v", account)
		}
		if account.CustomerID == operatorCustomer {
			t.Fatal("the operator's customer account was listed")
		}
	}
	if mine, err := operations.SearchSharedDevices(ctx, panelpg.SharedDeviceFilter{CustomerID: operatorCustomer}); err != nil || len(mine.Items) != 0 {
		t.Fatalf("operator filter = %+v (%v)", mine, err)
	}

	if state := harness.text(ctx, t, `SELECT review_state FROM referral_attributions WHERE referred_user_id = $1::uuid`, second); state != "held" {
		t.Fatalf("referral review state = %s, want held", state)
	}
	checkout := accountcheckout.NewStore(harness.pool)
	trial, err := checkout.TrialContext(ctx, second)
	if err != nil || !trial.SharedDeviceSignal {
		t.Fatalf("second account trial = %+v (%v), want the shared device signal", trial, err)
	}
	if trial, err = checkout.TrialContext(ctx, owner); err != nil || trial.SharedDeviceSignal {
		t.Fatalf("owner trial = %+v (%v): nobody else on the device took a trial", trial, err)
	}

	// A second scan over the same panel changes nothing and announces nothing.
	if result, err = scanner.Scan(ctx, now); err != nil || result.NewlyOpen != 0 || result.ReferralsHeld != 0 {
		t.Fatalf("rescan = %+v (%v)", result, err)
	}
	if count := harness.text(ctx, t, `SELECT count(*)::text FROM operator_notifications
		WHERE kind = 'monitoring' AND dedupe_key LIKE 'shared_devices:%'`); count != "1" {
		t.Fatalf("notifications = %s, want 1", count)
	}

	// The verdict needs a reason, then lifts the trial refusal.
	reviewer := harness.operator(ctx, t, "devices@example.test")
	if _, err = operations.ReviewSharedDevice(ctx, "phoneHandle01", "legitimate", panelpg.Actor{AdminID: reviewer.AdminID}); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("verdict without a reason: %v", err)
	}
	if _, err = operations.ReviewSharedDevice(ctx, "unknownHandle", "legitimate", reviewer); !errors.Is(err, panelpg.ErrNotFound) {
		t.Fatalf("verdict on an unknown device: %v", err)
	}
	reviewed, err := operations.ReviewSharedDevice(ctx, "phoneHandle01", "legitimate", reviewer)
	if err != nil || reviewed.Status != "legitimate" || reviewed.ReviewedAccounts != 2 {
		t.Fatalf("verdict = %+v (%v)", reviewed, err)
	}
	if _, err = operations.ReviewSharedDevice(ctx, "phoneHandle01", "confirmed", reviewer); !errors.Is(err, panelpg.ErrRejected) {
		t.Fatalf("second verdict on a reviewed device: %v", err)
	}
	if trial, err = checkout.TrialContext(ctx, second); err != nil || trial.SharedDeviceSignal {
		t.Fatalf("trial after a legitimate verdict = %+v (%v)", trial, err)
	}
	if count := harness.text(ctx, t, `SELECT count(*)::text FROM audit_events WHERE action = 'panel.shared_device.reviewed'`); count != "1" {
		t.Fatalf("audit events = %s, want 1", count)
	}

	// A third account on the device reopens it, keeping the earlier verdict.
	third := harness.customer(ctx, t)
	harness.exec(ctx, t, `INSERT INTO subscriptions (user_id, slot, label, remnawave_user_id) VALUES ($1::uuid, 1, 'Phone', 104)`, third)
	panel.devices = append(panel.devices, remnawave.PanelDevice{UserID: 104, Handle: "phoneHandle01", UpdatedAt: now})
	if result, err = scanner.Scan(ctx, now); err != nil || result.NewlyOpen != 1 {
		t.Fatalf("scan with a third account = %+v (%v)", result, err)
	}
	page, err = operations.SearchSharedDevices(ctx, panelpg.SharedDeviceFilter{Status: "open"})
	if err != nil || len(page.Items) != 1 || page.Items[0].Accounts != 3 ||
		page.Items[0].ReviewedAccounts != 2 || page.Items[0].ReviewReason == "" {
		t.Fatalf("reopened = %+v (%v)", page, err)
	}

	// Sightings older than the retention period are forgotten, and a device
	// left with one customer leaves the queue.
	if _, err = scanner.Scan(ctx, now.Add(remnawavesync.SightingRetention+2*time.Hour)); err != nil {
		t.Fatalf("scan after retention: %v", err)
	}
	if count := harness.text(ctx, t, `SELECT count(*)::text FROM shared_devices`); count != "0" {
		t.Fatalf("shared devices after retention = %s, want 0", count)
	}
}
