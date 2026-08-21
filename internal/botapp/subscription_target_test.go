package botapp

import (
	"strings"
	"testing"
	"time"
)

func TestExpiredSubscriptionOfPlanRevivesOnlyAMatchingLapsedOne(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	subscriptions := []SubscriptionSummary{
		{ID: "active-pro", Found: true, PlanID: "pro", Status: "active", EndsAt: now.Add(48 * time.Hour)},
		{ID: "expired-basic", Found: true, PlanID: "basic", Status: "expired", EndsAt: now.Add(-72 * time.Hour)},
		{ID: "grace-pro", Found: true, PlanID: "pro", Status: "active", EndsAt: now.Add(-time.Hour), GracePeriod: 24 * time.Hour},
		{ID: "expired-pro", Found: true, PlanID: "pro", Status: "active", EndsAt: now.Add(-48 * time.Hour)},
		{ID: "empty-slot", Found: false},
	}
	if got := expiredSubscriptionOfPlan(subscriptions, "basic", now); got != "expired-basic" {
		t.Fatalf("an expired subscription of the plan must be revived, got %q", got)
	}
	if got := expiredSubscriptionOfPlan(subscriptions, "pro", now); got != "expired-pro" {
		t.Fatalf("a live or grace-period subscription must not be revived; the lapsed one must, got %q", got)
	}
	if got := expiredSubscriptionOfPlan(subscriptions, "ultra", now); got != "" {
		t.Fatalf("a plan the customer never held opens a new subscription, got %q", got)
	}
	if got := expiredSubscriptionOfPlan(subscriptions, "", now); got != "" {
		t.Fatalf("an unknown plan must not match an empty plan id, got %q", got)
	}
}

func TestHomeKeyboardOffersToRestoreALapsedSubscription(t *testing.T) {
	t.Parallel()
	menu := MenuState{CommerceEnabled: true, RecoverSubscriptionID: "sub-1"}
	keyboard := commerceMainKeyboard(LocaleEnglish, menu)
	view := View{Keyboard: keyboard}
	callbacks := callbackData(view)
	if callbacks[0] != actionPrefix+"sub-renew:sub-1" {
		t.Fatalf("restoring access must be the first tap on the home screen: %v", callbacks)
	}
	if !strings.Contains(buttonLabels(view), "Restore subscription") {
		t.Fatalf("the restore button must say what it does: %s", buttonLabels(view))
	}
	healthy := View{Keyboard: commerceMainKeyboard(LocaleEnglish, MenuState{CommerceEnabled: true})}
	if strings.Contains(strings.Join(callbackData(healthy), " "), "sub-renew:") {
		t.Fatal("a healthy subscription has nothing to restore")
	}
}

func TestNewSubscriptionIntentSurvivesFromCatalogueToCheckout(t *testing.T) {
	t.Parallel()
	plan := Plan{PlanVersionID: "pv", Name: "Pro", Currency: "RUB", AmountMinor: 100, UpgradePolicy: "extend", DowngradePolicy: "immediate"}
	catalogue := plansView(LocaleEnglish, []Plan{plan}, "RUB", true)
	if !strings.Contains(strings.Join(callbackData(catalogue), " "), actionPrefix+"plan:pv:"+newSubscriptionFlag) {
		t.Fatalf("the catalogue opened for a new subscription must mark each plan: %v", callbackData(catalogue))
	}
	held := Entitlement{Found: true, PlanVersionID: "other-v", Status: "active", EndsAt: time.Now().Add(time.Hour), AmountMinor: 50}
	detail := planView(LocaleEnglish, plan, held, "", "", true)
	callbacks := callbackData(detail)
	if callbacks[0] != actionPrefix+"buy:pv:purchase:"+newSubscriptionFlag {
		t.Fatalf("a new subscription is always a purchase, flagged as new: %v", callbacks)
	}
	for _, data := range callbacks {
		if strings.Contains(data, ":upgrade") || strings.Contains(data, ":downgrade") || strings.Contains(data, ":extension") {
			t.Fatalf("there is nothing to switch or extend when adding a subscription: %v", callbacks)
		}
	}
	plain := plansView(LocaleEnglish, []Plan{plan}, "RUB", false)
	if strings.Contains(strings.Join(callbackData(plain), " "), newSubscriptionFlag) {
		t.Fatalf("the ordinary catalogue must not carry the flag: %v", callbackData(plain))
	}
}
