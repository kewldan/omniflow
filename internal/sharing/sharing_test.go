package sharing

import (
	"reflect"
	"testing"
	"time"
)

func TestAssess(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-time.Hour)
	if signal := Assess([]Device{{Platform: "iOS", Model: "iPhone", LastSeen: recent}, {Platform: "Windows", LastSeen: recent}}, now); signal.Level != "none" || signal.ActiveDevices != 2 {
		t.Fatalf("one person = %+v", signal)
	}
	shared := []Device{
		{Platform: "Android", Model: "Pixel 8", LastSeen: recent}, {Platform: "android", Model: "Galaxy S24", LastSeen: recent},
		{Platform: "Android", Model: "Redmi 13", LastSeen: recent}, {Platform: "iOS", LastSeen: recent},
		{Platform: "Windows", LastSeen: recent}, {Platform: "macOS", LastSeen: recent},
		{Platform: "Linux", LastSeen: now.Add(-72 * time.Hour)},
	}
	signal := Assess(shared, now)
	want := []string{"many_active_devices", "many_models_of_one_platform", "many_platforms"}
	if signal.Level != "likely" || signal.ActiveDevices != 6 || !reflect.DeepEqual(signal.Reasons, want) {
		t.Fatalf("shared = %+v", signal)
	}
}
