//go:build integration

package integrationtest

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/remnawave"
	"github.com/omniflow/omniflow/internal/remnawavesync"
)

type monitorPanel struct {
	users   []remnawave.User
	devices []remnawave.PanelDevice
}

func (panel *monitorPanel) ListUsers(_ context.Context, start, size int) ([]remnawave.User, int, error) {
	end := min(start+size, len(panel.users))
	if start >= end {
		return nil, len(panel.users), nil
	}
	return panel.users[start:end], len(panel.users), nil
}

func (panel *monitorPanel) AllDevices(_ context.Context, start, size int) ([]remnawave.PanelDevice, int, error) {
	end := min(start+size, len(panel.devices))
	if start >= end {
		return nil, len(panel.devices), nil
	}
	return panel.devices[start:end], len(panel.devices), nil
}

type memorySnapshots struct {
	readings map[string]map[int64]int64
	claimed  map[string]bool
}

func (store *memorySnapshots) Load(_ context.Context, key string) (map[int64]int64, error) {
	if reading, ok := store.readings[key]; ok {
		return reading, nil
	}
	return map[int64]int64{}, nil
}

func (store *memorySnapshots) Save(_ context.Context, key string, counters map[int64]int64, _ time.Duration) error {
	store.readings[key] = counters
	return nil
}

func (store *memorySnapshots) Once(_ context.Context, key string, _ time.Duration) (bool, error) {
	if store.claimed[key] {
		return false, nil
	}
	store.claimed[key] = true
	return true, nil
}

// The fast pass takes a reading first, reports only users whose counter grew
// past the threshold inside the filters, and does not report the same user
// again inside the cooldown. The device report counts recently active devices
// against each user's limit.
func TestUsageMonitoringReportsSpikesAndDeviceOverflow(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	const gigabyte = 1 << 30
	two := 2
	panel := &monitorPanel{users: []remnawave.User{
		{ID: 1, Status: "ACTIVE", HWIDDeviceLimit: &two, Traffic: remnawave.UserTraffic{UsedBytes: 0, LastConnectedNodeUUID: "node-a"}},
		{ID: 2, Status: "ACTIVE", Traffic: remnawave.UserTraffic{UsedBytes: 0, LastConnectedNodeUUID: "node-b"}},
		{ID: 3, Status: "ACTIVE", Traffic: remnawave.UserTraffic{UsedBytes: 0, LastConnectedNodeUUID: "node-a"}},
	}}
	store := &memorySnapshots{readings: map[string]map[int64]int64{}, claimed: map[string]bool{}}
	settings := remnawavesync.MonitorSettings{
		IgnoredNodes: map[string]bool{"node-b": true}, ExcludedUsers: map[int64]bool{3: true}, Cooldown: time.Hour,
	}
	monitor := remnawavesync.NewMonitor(harness.pool, panel, store, settings, slog.New(slog.DiscardHandler))

	if err := monitor.TrafficPass(ctx, "fast", 5*gigabyte, time.Hour); err != nil {
		t.Fatal(err)
	}
	for index := range panel.users {
		panel.users[index].Traffic.UsedBytes = 6 * gigabyte
	}
	if err := monitor.TrafficPass(ctx, "fast", 5*gigabyte, time.Hour); err != nil {
		t.Fatal(err)
	}
	for index := range panel.users {
		panel.users[index].Traffic.UsedBytes = 12 * gigabyte
	}
	if err := monitor.TrafficPass(ctx, "fast", 5*gigabyte, time.Hour); err != nil {
		t.Fatal(err)
	}
	rows, err := harness.pool.Query(ctx, `SELECT payload->>'users' FROM operator_notifications
		WHERE kind = 'monitoring' AND payload->>'event' = 'traffic_spike'`)
	if err != nil {
		t.Fatal(err)
	}
	var reported []string
	for rows.Next() {
		var users string
		_ = rows.Scan(&users)
		reported = append(reported, users)
	}
	rows.Close()
	if len(reported) != 1 || reported[0] != "1" {
		t.Fatalf("spike reports = %v, want one report naming only user 1", reported)
	}

	now := time.Now()
	panel.devices = []remnawave.PanelDevice{
		{UserID: 1, UpdatedAt: now}, {UserID: 1, UpdatedAt: now}, {UserID: 1, UpdatedAt: now},
		{UserID: 1, UpdatedAt: now.Add(-40 * 24 * time.Hour)}, {UserID: 2, UpdatedAt: now},
	}
	if err := monitor.DeviceOverflowPass(ctx, now); err != nil {
		t.Fatal(err)
	}
	var devices string
	if err := harness.pool.QueryRow(ctx, `SELECT payload->>'devices' FROM operator_notifications
		WHERE payload->>'event' = 'devices_over_limit'`).Scan(&devices); err != nil || !strings.Contains(devices, "1:3/2") {
		t.Fatalf("overflow report = %q (%v)", devices, err)
	}
}
