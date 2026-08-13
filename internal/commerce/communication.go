package commerce

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// RenderTemplate substitutes a message template's declared variables.
//
// A variable with no value renders as an empty string rather than leaving the
// placeholder visible: a customer seeing nothing is better than a customer
// seeing `{{name}}`, and the validation performed when a template is saved is
// what stops an undeclared variable reaching here at all.
//
// It lives in the domain rather than beside one of its callers because there
// are two, and they must agree. The bot renders what a customer receives; the
// operator preview renders what the operator is shown before committing to the
// audience. A preview that substituted differently would be a preview of a
// different message, which is the one thing it must never be.
func RenderTemplate(body string, values map[string]string) string {
	for name, value := range values {
		body = strings.ReplaceAll(body, "{{"+name+"}}", value)
		body = strings.ReplaceAll(body, "{{ "+name+" }}", value)
	}
	return templateVariablePattern.ReplaceAllString(body, "")
}

// templateVariablePattern matches any placeholder left after substitution.
var templateVariablePattern = regexp.MustCompile(`\{\{\s*[a-zA-Z][a-zA-Z0-9_]*\s*\}\}`)

// MessageClass separates messages a customer must always receive from messages
// that require explicit marketing consent.
type MessageClass string

const (
	ClassTransactional MessageClass = "transactional"
	ClassMarketing     MessageClass = "marketing"
)

var (
	ErrUnknownNotification = errors.New("unknown notification kind")
	ErrDeliverySuppressed  = errors.New("notification delivery is suppressed")
)

// notificationClasses is the complete classification of customer notifications.
// Adding a kind without classifying it is a compile-time-visible omission
// because ClassifyNotification refuses unknown kinds.
var notificationClasses = map[string]MessageClass{
	"expiry":       ClassTransactional,
	"traffic":      ClassTransactional,
	"renewal":      ClassTransactional,
	"grace":        ClassTransactional,
	"recovery":     ClassTransactional,
	"payment":      ClassTransactional,
	"fulfillment":  ClassTransactional,
	"support":      ClassTransactional,
	"incident":     ClassTransactional,
	"maintenance":  ClassTransactional,
	"announcement": ClassTransactional,
	"referral":     ClassTransactional,
	"trial":        ClassTransactional,
	"news":         ClassMarketing,
	"marketing":    ClassMarketing,
}

// urgentNotifications are never deferred by quiet hours: the customer is either
// mid-transaction or actively affected by an incident.
var urgentNotifications = map[string]bool{
	"payment":     true,
	"fulfillment": true,
	"support":     true,
	"incident":    true,
}

// ClassifyNotification reports whether a notification kind is transactional or
// marketing.
func ClassifyNotification(kind string) (MessageClass, error) {
	class, found := notificationClasses[kind]
	if !found {
		return "", ErrUnknownNotification
	}
	return class, nil
}

// QuietHours is a local-time window during which non-urgent messages wait.
type QuietHours struct {
	Configured bool
	StartHour  int
	EndHour    int
	Location   *time.Location
}

func (hours QuietHours) location() *time.Location {
	if hours.Location == nil {
		return time.UTC
	}
	return hours.Location
}

// Contains reports whether the instant falls inside the quiet window. A window
// whose end is not after its start wraps past midnight.
func (hours QuietHours) Contains(moment time.Time) bool {
	if !hours.Configured || hours.StartHour == hours.EndHour {
		return false
	}
	hour := moment.In(hours.location()).Hour()
	if hours.StartHour < hours.EndHour {
		return hour >= hours.StartHour && hour < hours.EndHour
	}
	return hour >= hours.StartHour || hour < hours.EndHour
}

// Ends returns the first instant at or after the given moment that falls outside
// the quiet window.
func (hours QuietHours) Ends(moment time.Time) time.Time {
	if !hours.Contains(moment) {
		return moment
	}
	local := moment.In(hours.location())
	end := time.Date(local.Year(), local.Month(), local.Day(), hours.EndHour, 0, 0, 0, hours.location())
	if !end.After(local) {
		end = end.AddDate(0, 0, 1)
	}
	return end.UTC()
}

// DeliveryPolicy is everything the domain needs to decide whether a customer
// notification may be sent right now.
type DeliveryPolicy struct {
	// KindEnabled is the customer's per-kind notification preference.
	KindEnabled bool
	// MarketingConsent is the explicit, revocable marketing opt-in.
	MarketingConsent bool
	QuietHours       QuietHours
	// MarketingSentInWindow counts marketing messages already delivered inside
	// FrequencyWindow, and MarketingFrequencyCap bounds it. A cap of zero or
	// less disables the cap.
	MarketingSentInWindow int
	MarketingFrequencyCap int
	FrequencyWindow       time.Duration
	// DeliveryStatus mirrors bot_delivery_state.status.
	DeliveryStatus string
	// RetryAfter defers delivery after a Telegram flood wait.
	RetryAfter time.Time
}

// DeliveryDecision explains what should happen to one notification.
type DeliveryDecision struct {
	Allow      bool
	DeferUntil time.Time
	Class      MessageClass
	Reason     string
}

// EvaluateDelivery applies consent, per-kind preferences, recipient reachability,
// quiet hours, and frequency caps. Suppressed marketing is dropped; a message
// that is merely early is deferred so it is neither lost nor sent at a bad hour.
func EvaluateDelivery(now time.Time, kind string, policy DeliveryPolicy) (DeliveryDecision, error) {
	class, err := ClassifyNotification(kind)
	if err != nil {
		return DeliveryDecision{}, err
	}
	decision := DeliveryDecision{Class: class}
	switch policy.DeliveryStatus {
	case "blocked":
		decision.Reason = "bot_blocked"
		return decision, nil
	case "deactivated":
		decision.Reason = "user_deactivated"
		return decision, nil
	}
	if !policy.KindEnabled {
		decision.Reason = "kind_disabled"
		return decision, nil
	}
	if class == ClassMarketing {
		if !policy.MarketingConsent {
			decision.Reason = "no_marketing_consent"
			return decision, nil
		}
		if policy.MarketingFrequencyCap > 0 && policy.MarketingSentInWindow >= policy.MarketingFrequencyCap {
			decision.Reason = "frequency_cap"
			return decision, nil
		}
	}
	deferUntil := time.Time{}
	if policy.RetryAfter.After(now) {
		deferUntil = policy.RetryAfter
	}
	if !urgentNotifications[kind] {
		if quietEnd := policy.QuietHours.Ends(maxTime(now, deferUntil)); quietEnd.After(now) {
			deferUntil = quietEnd
		}
	}
	if deferUntil.After(now) {
		decision.DeferUntil = deferUntil
		decision.Reason = "deferred"
		return decision, nil
	}
	decision.Allow = true
	decision.Reason = "allowed"
	return decision, nil
}

// ClassifyTelegramFailure maps a Telegram delivery failure onto the durable
// delivery state so a blocked or deleted account stops consuming retries.
func ClassifyTelegramFailure(code string) (status string, retryable bool) {
	switch code {
	case "bot_blocked":
		return "blocked", false
	case "user_deactivated", "chat_not_found":
		return "deactivated", false
	case "flood_wait", "telegram_unavailable", "timeout":
		return "failing", true
	default:
		return "failing", true
	}
}

func maxTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}
