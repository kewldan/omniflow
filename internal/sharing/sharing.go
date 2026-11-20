// Package sharing reads signs that one subscription is used by several
// people, from what Remnawave already reports about its devices.
//
// It is read-only by decision 0013: Remnawave owns devices and their limits,
// so Omniflow neither stores device history nor acts on the signal. The
// signal is computed when an operator opens a subscription's devices and is
// shown to them beside the list; nothing is blocked, removed, or messaged.
package sharing

import (
	"sort"
	"strings"
	"time"
)

// Device is the part of a Remnawave device report the signal reads.
type Device struct {
	Platform string
	Model    string
	LastSeen time.Time
}

// Signal is the assessment.
type Signal struct {
	Level   string   `json:"level"`
	Reasons []string `json:"reasons"`
	// ActiveDevices is how many devices were seen in the last day.
	ActiveDevices int `json:"activeDevices"`
}

// activeWindow is how recent a device must be to count as in use.
const activeWindow = 24 * time.Hour

// Assess reads the device list. The reasons are the patterns Bedolaga's
// operators look for by hand: more devices active in a day than one person
// carries, many different platforms, and many different models of one
// platform.
func Assess(devices []Device, now time.Time) Signal {
	signal := Signal{Reasons: []string{}}
	platforms := map[string]bool{}
	models := map[string]map[string]bool{}
	for _, device := range devices {
		if now.Sub(device.LastSeen) > activeWindow {
			continue
		}
		signal.ActiveDevices++
		platform := strings.ToLower(strings.TrimSpace(device.Platform))
		if platform == "" {
			continue
		}
		platforms[platform] = true
		if models[platform] == nil {
			models[platform] = map[string]bool{}
		}
		if model := strings.ToLower(strings.TrimSpace(device.Model)); model != "" {
			models[platform][model] = true
		}
	}
	if signal.ActiveDevices >= 5 {
		signal.Reasons = append(signal.Reasons, "many_active_devices")
	}
	if len(platforms) >= 4 {
		signal.Reasons = append(signal.Reasons, "many_platforms")
	}
	for _, seen := range models {
		if len(seen) >= 3 {
			signal.Reasons = append(signal.Reasons, "many_models_of_one_platform")
			break
		}
	}
	sort.Strings(signal.Reasons)
	switch len(signal.Reasons) {
	case 0:
		signal.Level = "none"
	case 1:
		signal.Level = "possible"
	default:
		signal.Level = "likely"
	}
	return signal
}
