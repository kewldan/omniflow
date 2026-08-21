package botapp

import (
	"strings"
	"testing"
)

func TestOnlyAnActiveOrLegacyCustomerIsActive(t *testing.T) {
	t.Parallel()
	for status, want := range map[string]bool{"": true, "active": true, "suspended": false, "deleted": false} {
		if got := accountActive(Customer{Status: status}); got != want {
			t.Fatalf("status %q: active = %v, want %v", status, got, want)
		}
	}
}

func TestAnInactiveCustomerKeepsOnlyTheSupportDesk(t *testing.T) {
	t.Parallel()
	for _, name := range []string{routeSupport, "ticket", "ticket-reply", "support-new"} {
		if !allowedWhileInactive(name) {
			t.Fatalf("%q must stay reachable: support is how a suspended customer talks to an operator", name)
		}
	}
	for _, name := range []string{routeHome, routePlans, routeWallet, routeSubscription, routeDevices, routeReferral, "confirm", "pay", "revoke", "device-delete", "cart-buy", "autorenew", "ticket-close"} {
		if allowedWhileInactive(name) {
			t.Fatalf("%q must be closed to a suspended customer", name)
		}
	}
}

func TestAccountUnavailableViewNamesTheStateAndOffersSupport(t *testing.T) {
	t.Parallel()
	suspended := accountUnavailableView(LocaleEnglish, Customer{Status: "suspended"}, "https://t.me/helpdesk")
	if !strings.Contains(suspended.Text, "Account suspended") {
		t.Fatalf("a suspended account must be named as such: %s", suspended.Text)
	}
	if !strings.Contains(strings.Join(callbackData(suspended), " "), callbackPrefix+routeSupport) {
		t.Fatalf("the support desk must be one tap away: %v", callbackData(suspended))
	}
	if !strings.Contains(buttonLabels(suspended), "Contact support") {
		t.Fatalf("the operator's support link must be offered when configured: %s", buttonLabels(suspended))
	}
	deleted := accountUnavailableView(LocaleRussian, Customer{Status: "deleted"}, "")
	if !strings.Contains(deleted.Text, "Аккаунт удалён") {
		t.Fatalf("a deleted account reads differently from a suspended one: %s", deleted.Text)
	}
	if strings.Contains(buttonLabels(deleted), "Написать в поддержку") {
		t.Fatal("no support URL, no URL button")
	}
}
