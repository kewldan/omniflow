package remnawavesync

import (
	"testing"
	"time"
)

func TestMonitorSettingsFromEnv(t *testing.T) {
	env := map[string]string{
		"APP_TRAFFIC_MONITOR_FAST_THRESHOLD_GB":  "5",
		"APP_TRAFFIC_MONITOR_DAILY_THRESHOLD_GB": "50.5",
		"APP_TRAFFIC_MONITOR_NODES":              " Node-A ,node-b",
		"APP_TRAFFIC_MONITOR_EXCLUDED_USERS":     "7, 9",
		"APP_DEVICE_OVERFLOW_REPORT":             "true",
	}
	settings, err := MonitorSettingsFromEnv(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if settings.FastInterval != 10*time.Minute || settings.FastThresholdBytes != 5<<30 ||
		settings.DailyThresholdBytes != int64(50.5*(1<<30)) || !settings.MonitoredNodes["node-a"] ||
		!settings.ExcludedUsers[9] || !settings.DeviceReport || settings.Cooldown != time.Hour {
		t.Fatalf("settings = %+v", settings)
	}
	off, _ := MonitorSettingsFromEnv(func(string) string { return "" })
	if off.FastInterval != 0 || off.DailyThresholdBytes != 0 || off.DeviceReport {
		t.Fatalf("everything is off by default: %+v", off)
	}
	for _, bad := range []map[string]string{
		{"APP_TRAFFIC_MONITOR_FAST_THRESHOLD_GB": "lots"},
		{"APP_TRAFFIC_MONITOR_FAST_INTERVAL_MINUTES": "0"},
		{"APP_TRAFFIC_MONITOR_EXCLUDED_USERS": "abc"},
	} {
		if _, err := MonitorSettingsFromEnv(func(key string) string { return bad[key] }); err == nil {
			t.Fatalf("%v was accepted", bad)
		}
	}
}
