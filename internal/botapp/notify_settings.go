package botapp

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// NotificationSettings are the operator's thresholds and switches for the
// lifecycle notices: on which days before the end a reminder goes out, at
// which share of the traffic allowance a warning does, and which kinds of
// notice this installation does not send at all.
//
// A customer's own preferences still apply on top: a kind switched on here is
// only sent to a customer who has not turned it off for themselves.
type NotificationSettings struct {
	// ExpiryDays are the whole days before the end on which the expiry and
	// renewal reminders are sent. Default 7, 3, 1, 0.
	ExpiryDays []int
	// TrafficPercents are the shares of the allowance that trigger a traffic
	// warning. Default 80, 100.
	TrafficPercents []int
	// Disabled names the kinds this installation never sends.
	Disabled map[string]bool
}

// switchableKinds are the notices an operator can turn off. Payment, support,
// and the operator's own messages are consequences of something the customer
// or the operator did and are never switchable.
var switchableKinds = []string{
	"expiry", "traffic", "renewal", "grace", "recovery", "fulfillment", "trial",
	"connection", "device", "access",
}

// DefaultNotificationSettings is what an installation that set nothing sends.
func DefaultNotificationSettings() NotificationSettings {
	return NotificationSettings{ExpiryDays: []int{7, 3, 1, 0}, TrafficPercents: []int{80, 100}}
}

// NotificationSettingsFromEnv reads APP_NOTIFY_EXPIRY_DAYS,
// APP_NOTIFY_TRAFFIC_PERCENTS, and APP_NOTIFY_DISABLED.
func NotificationSettingsFromEnv(getenv func(string) string) (NotificationSettings, error) {
	settings := DefaultNotificationSettings()
	if raw := strings.TrimSpace(getenv("APP_NOTIFY_EXPIRY_DAYS")); raw != "" {
		days, err := numberList(raw, 0, 60)
		if err != nil {
			return NotificationSettings{}, fmt.Errorf("APP_NOTIFY_EXPIRY_DAYS: %w", err)
		}
		settings.ExpiryDays = days
	}
	if raw := strings.TrimSpace(getenv("APP_NOTIFY_TRAFFIC_PERCENTS")); raw != "" {
		percents, err := numberList(raw, 1, 100)
		if err != nil {
			return NotificationSettings{}, fmt.Errorf("APP_NOTIFY_TRAFFIC_PERCENTS: %w", err)
		}
		settings.TrafficPercents = percents
	}
	for _, kind := range strings.Split(getenv("APP_NOTIFY_DISABLED"), ",") {
		kind = strings.ToLower(strings.TrimSpace(kind))
		if kind == "" {
			continue
		}
		if !slices.Contains(switchableKinds, kind) {
			return NotificationSettings{}, fmt.Errorf(
				"APP_NOTIFY_DISABLED: %q is not one of %s", kind, strings.Join(switchableKinds, ", "),
			)
		}
		if settings.Disabled == nil {
			settings.Disabled = map[string]bool{}
		}
		settings.Disabled[kind] = true
	}
	return settings, nil
}

// numberList parses "7, 3,1" into distinct values within bounds, sorted.
func numberList(raw string, lowest, highest int) ([]int, error) {
	var values []int
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		value, err := strconv.Atoi(part)
		if err != nil || value < lowest || value > highest {
			return nil, fmt.Errorf("%q is not a whole number from %d to %d", part, lowest, highest)
		}
		if !slices.Contains(values, value) {
			values = append(values, value)
		}
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("list is empty")
	}
	slices.Sort(values)
	return values, nil
}

// enabled reports whether this installation sends the kind at all.
func (settings NotificationSettings) enabled(kind string) bool {
	return !settings.Disabled[kind]
}

// expiryDue reports whether a reminder is due with this many days left.
func (settings NotificationSettings) expiryDue(days int) bool {
	list := settings.ExpiryDays
	if len(list) == 0 {
		list = DefaultNotificationSettings().ExpiryDays
	}
	return slices.Contains(list, days)
}

// trafficThreshold returns the highest configured threshold the usage has
// reached, or zero when it has reached none.
func (settings NotificationSettings) trafficThreshold(percent int64) int64 {
	list := settings.TrafficPercents
	if len(list) == 0 {
		list = DefaultNotificationSettings().TrafficPercents
	}
	reached := int64(0)
	for _, threshold := range list {
		if percent >= int64(threshold) {
			reached = int64(threshold)
		}
	}
	return reached
}

// renewalReminderDue is commerce.RenewalReminderDue over the operator's days:
// whole days remaining, floored, so the last reminder fires on the last day.
func renewalReminderDue(now, endsAt time.Time, settings NotificationSettings) (int, bool) {
	if endsAt.IsZero() || endsAt.Before(now) {
		return 0, false
	}
	days := int(endsAt.Sub(now) / (24 * time.Hour))
	return days, settings.expiryDue(days)
}
