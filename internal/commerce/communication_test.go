package commerce

import (
	"errors"
	"testing"
	"time"
)

func TestClassifyNotificationSeparatesMarketing(t *testing.T) {
	for kind, expected := range map[string]MessageClass{
		"payment": ClassTransactional,
		"expiry":  ClassTransactional,
		"support": ClassTransactional,
		"news":    ClassMarketing,
	} {
		class, err := ClassifyNotification(kind)
		if err != nil {
			t.Fatalf("classify %q: %v", kind, err)
		}
		if class != expected {
			t.Fatalf("classify %q = %q, want %q", kind, class, expected)
		}
	}
	if _, err := ClassifyNotification("unclassified"); !errors.Is(err, ErrUnknownNotification) {
		t.Fatalf("unknown kind error = %v, want ErrUnknownNotification", err)
	}
}

func TestEvaluateDeliverySuppressesMarketingWithoutConsent(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	decision, err := EvaluateDelivery(now, "news", DeliveryPolicy{KindEnabled: true, DeliveryStatus: "active"})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.Allow || decision.Reason != "no_marketing_consent" {
		t.Fatalf("decision = %+v, want suppressed for missing consent", decision)
	}
}

func TestEvaluateDeliveryAppliesFrequencyCap(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	policy := DeliveryPolicy{KindEnabled: true, MarketingConsent: true, DeliveryStatus: "active", MarketingSentInWindow: 2, MarketingFrequencyCap: 2, FrequencyWindow: 7 * 24 * time.Hour}
	decision, err := EvaluateDelivery(now, "news", policy)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.Allow || decision.Reason != "frequency_cap" {
		t.Fatalf("decision = %+v, want frequency cap suppression", decision)
	}
	policy.MarketingSentInWindow = 1
	decision, err = EvaluateDelivery(now, "news", policy)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !decision.Allow {
		t.Fatalf("decision = %+v, want delivery below the cap", decision)
	}
}

func TestEvaluateDeliveryDefersNonUrgentDuringQuietHours(t *testing.T) {
	now := time.Date(2026, 8, 11, 23, 30, 0, 0, time.UTC)
	policy := DeliveryPolicy{KindEnabled: true, DeliveryStatus: "active", QuietHours: QuietHours{Configured: true, StartHour: 22, EndHour: 8}}
	decision, err := EvaluateDelivery(now, "renewal", policy)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	expected := time.Date(2026, 8, 12, 8, 0, 0, 0, time.UTC)
	if decision.Allow || !decision.DeferUntil.Equal(expected) {
		t.Fatalf("decision = %+v, want deferral to %s", decision, expected)
	}
	urgent, err := EvaluateDelivery(now, "payment", policy)
	if err != nil {
		t.Fatalf("evaluate urgent: %v", err)
	}
	if !urgent.Allow {
		t.Fatalf("urgent decision = %+v, want immediate delivery", urgent)
	}
}

func TestEvaluateDeliveryStopsUnreachableRecipients(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	for status, reason := range map[string]string{"blocked": "bot_blocked", "deactivated": "user_deactivated"} {
		decision, err := EvaluateDelivery(now, "payment", DeliveryPolicy{KindEnabled: true, DeliveryStatus: status})
		if err != nil {
			t.Fatalf("evaluate %q: %v", status, err)
		}
		if decision.Allow || decision.Reason != reason || !decision.DeferUntil.IsZero() {
			t.Fatalf("decision for %q = %+v, want a permanent stop", status, decision)
		}
	}
}

func TestClassifyTelegramFailureStopsRetryingBlockedAccounts(t *testing.T) {
	for code, wantRetry := range map[string]bool{"bot_blocked": false, "user_deactivated": false, "flood_wait": true, "telegram_unavailable": true} {
		_, retryable := ClassifyTelegramFailure(code)
		if retryable != wantRetry {
			t.Fatalf("retryable(%q) = %t, want %t", code, retryable, wantRetry)
		}
	}
}

func TestQuietHoursWrapsMidnight(t *testing.T) {
	hours := QuietHours{Configured: true, StartHour: 22, EndHour: 8}
	if !hours.Contains(time.Date(2026, 8, 11, 2, 0, 0, 0, time.UTC)) {
		t.Fatal("02:00 must fall inside a 22:00-08:00 window")
	}
	if hours.Contains(time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)) {
		t.Fatal("09:00 must fall outside a 22:00-08:00 window")
	}
}
