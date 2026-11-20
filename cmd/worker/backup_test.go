package main

import (
	"testing"

	"github.com/omniflow/omniflow/internal/installsettings"
)

func TestBackupSchedules(t *testing.T) {
	cases := []struct {
		name            string
		section         installsettings.Document
		slot            int
		daily, interval bool
	}{
		{"nothing saved uses the environment interval", installsettings.Document{}, 0, false, true},
		{"an interval alone", installsettings.Document{"intervalHours": float64(6)}, 0, false, true},
		{"a time of day alone", installsettings.Document{"timeOfDay": "03:30"}, 210, true, false},
		{"both run side by side", installsettings.Document{"timeOfDay": "03:30", "intervalHours": "12"}, 210, true, true},
		{"a malformed time falls back to the interval", installsettings.Document{"timeOfDay": "25:99"}, 0, false, true},
	}
	for _, item := range cases {
		slot, daily, interval := backupSchedules(item.section)
		if daily != item.daily || interval != item.interval || daily && slot != item.slot {
			t.Errorf("%s: got slot=%d daily=%v interval=%v", item.name, slot, daily, interval)
		}
	}
}
