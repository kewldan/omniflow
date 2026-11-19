package botapp

import (
	"testing"
	"time"
)

func TestNotificationSettingsFromEnv(t *testing.T) {
	env := map[string]string{
		"APP_NOTIFY_EXPIRY_DAYS":      "5, 2,2, 0",
		"APP_NOTIFY_TRAFFIC_PERCENTS": "90,50",
		"APP_NOTIFY_DISABLED":         "grace, Recovery",
	}
	settings, err := NotificationSettingsFromEnv(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if !settings.expiryDue(5) || !settings.expiryDue(0) || settings.expiryDue(7) {
		t.Fatalf("expiry days = %v", settings.ExpiryDays)
	}
	if settings.trafficThreshold(49) != 0 || settings.trafficThreshold(75) != 50 || settings.trafficThreshold(120) != 90 {
		t.Fatalf("traffic thresholds = %v", settings.TrafficPercents)
	}
	if settings.enabled("grace") || settings.enabled("recovery") || !settings.enabled("expiry") {
		t.Fatalf("disabled = %v", settings.Disabled)
	}
}

func TestNotificationSettingsRefuseNonsense(t *testing.T) {
	for _, env := range []map[string]string{
		{"APP_NOTIFY_EXPIRY_DAYS": "seven"},
		{"APP_NOTIFY_TRAFFIC_PERCENTS": "0"},
		{"APP_NOTIFY_TRAFFIC_PERCENTS": "150"},
		{"APP_NOTIFY_DISABLED": "payment"},
		{"APP_NOTIFY_EXPIRY_DAYS": " , "},
	} {
		if _, err := NotificationSettingsFromEnv(func(key string) string { return env[key] }); err == nil {
			t.Fatalf("%v was accepted", env)
		}
	}
}

func TestTheZeroSettingsKeepTheShippedThresholds(t *testing.T) {
	var settings NotificationSettings
	for _, days := range []int{7, 3, 1, 0} {
		if !settings.expiryDue(days) {
			t.Fatalf("day %d is not due by default", days)
		}
	}
	if settings.trafficThreshold(85) != 80 || settings.trafficThreshold(100) != 100 {
		t.Fatal("default traffic thresholds changed")
	}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if days, due := renewalReminderDue(now, now.Add(3*24*time.Hour+time.Hour), settings); !due || days != 3 {
		t.Fatalf("renewal = %d %v", days, due)
	}
	if _, due := renewalReminderDue(now, now.Add(-time.Hour), settings); due {
		t.Fatal("an ended entitlement is not reminded")
	}
}
