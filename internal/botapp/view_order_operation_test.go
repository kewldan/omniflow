package botapp

import (
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
)

func TestPaidOrderScreenFollowsTheOperation(t *testing.T) {
	t.Parallel()
	paid := OrderSummary{ID: "abcdef12-0000-0000-0000-000000000000", State: commerce.OrderPaid, Currency: "RUB", SubtotalMinor: 50000, Phase: commerce.PaymentPhaseProvisioning}
	cases := []struct {
		name      string
		operation string
		extras    orderExtras
		text      string
		route     string
		forbidden string
	}{
		{"top-up is credited", "topup", orderExtras{}, "500 RUB", callbackPrefix + routeWallet, "Activating"},
		{"gift is redeemable", "gift", orderExtras{Gift: &SentGift{CodeHint: "7Q2K", Status: "deliverable"}}, "••7Q2K", callbackPrefix + routeGifts, "Activating"},
		{"gift without its row", "gift", orderExtras{}, "code you saved", callbackPrefix + routeGifts, "Activating"},
		{"goods are delivering", "goods", orderExtras{Shop: &ShopOrder{Status: "paid", DeliveryStatus: "queued"}}, "sending", callbackPrefix + routeShopOrders, "Activating"},
		{"goods under review", "goods", orderExtras{Shop: &ShopOrder{Status: "paid", DeliveryStatus: "needs_review"}}, "checking this delivery by hand", callbackPrefix + routeShopOrders, "Activating"},
		{"add-on is applying", "addon", orderExtras{}, "Applying your add-on", "", "subscription is active"},
		{"subscription is activating", "purchase", orderExtras{}, "Activating your subscription", "", "wallet"},
		{"renewal is activating", "extension", orderExtras{}, "Activating your subscription", "", "wallet"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			order := paid
			order.Operation = tc.operation
			view := orderView(LocaleEnglish, order, nil, tc.extras)
			if !strings.Contains(view.Text, tc.text) {
				t.Fatalf("missing %q: %s", tc.text, view.Text)
			}
			if strings.Contains(view.Text, tc.forbidden) {
				t.Fatalf("must not say %q: %s", tc.forbidden, view.Text)
			}
			if tc.route != "" && !strings.Contains(strings.Join(callbackData(view), " "), tc.route) {
				t.Fatalf("missing button %q: %v", tc.route, callbackData(view))
			}
		})
	}
}

func TestCompletedAddonAndSubscriptionOrdersDiffer(t *testing.T) {
	t.Parallel()
	done := OrderSummary{ID: "abcdef12-0000-0000-0000-000000000000", State: commerce.OrderFulfilled, Currency: "RUB", Phase: commerce.PaymentPhaseCompleted}
	addon := done
	addon.Operation = "addon"
	if view := orderView(LocaleEnglish, addon, nil, orderExtras{}); !strings.Contains(view.Text, "Add-on applied") || !strings.Contains(strings.Join(callbackData(view), " "), callbackPrefix+routeSubscriptions) {
		t.Fatalf("a completed add-on order names the add-on and leads to the subscription: %s %v", view.Text, callbackData(view))
	}
	plan := done
	plan.Operation = "purchase"
	if view := orderView(LocaleEnglish, plan, nil, orderExtras{}); !strings.Contains(view.Text, "All set") || !strings.Contains(strings.Join(callbackData(view), " "), callbackPrefix+routeConnect) {
		t.Fatalf("a completed subscription order leads to connect: %s %v", view.Text, callbackData(view))
	}
}

func TestProvisioningTroubleIsSurfaced(t *testing.T) {
	t.Parallel()
	slow := OrderSummary{ID: "abcdef12-0000-0000-0000-000000000000", State: commerce.OrderPaid, Operation: "purchase", Phase: commerce.PaymentPhaseProvisioning, FulfillmentAttempts: 3}
	if view := orderView(LocaleEnglish, slow, nil, orderExtras{}); !strings.Contains(view.Text, "taking longer than usual") {
		t.Fatalf("a retried provisioning run must say so: %s", view.Text)
	}
	first := slow
	first.FulfillmentAttempts = 1
	if view := orderView(LocaleEnglish, first, nil, orderExtras{}); strings.Contains(view.Text, "taking longer") {
		t.Fatalf("a first attempt is not slow: %s", view.Text)
	}
	failed := OrderSummary{ID: slow.ID, State: commerce.OrderPaid, Operation: "purchase", Phase: commerce.PaymentPhaseFailed, FulfillmentStatus: "failed"}
	view := orderView(LocaleEnglish, failed, nil, orderExtras{})
	if !strings.Contains(view.Text, "Activation did not finish") || strings.Contains(view.Text, "Nothing was charged") {
		t.Fatalf("a failed provisioning run on a paid order must not claim nothing was charged: %s", view.Text)
	}
	if !strings.Contains(strings.Join(callbackData(view), " "), callbackPrefix+routeSupport) {
		t.Fatalf("a failed provisioning run must offer support: %v", callbackData(view))
	}
	if strings.Contains(strings.Join(callbackData(view), " "), actionPrefix+"pay:") {
		t.Fatal("a paid order must not be offered another payment")
	}
}

func TestPendingGiftOrderSaysWhenItBecomesRedeemable(t *testing.T) {
	t.Parallel()
	order := OrderSummary{ID: "abcdef12-0000-0000-0000-000000000000", State: commerce.OrderPending, Operation: "gift", Currency: "RUB", ExternalMinor: 100, Phase: commerce.PaymentPhaseAwaitingAction}
	if view := orderView(LocaleRussian, order, nil, orderExtras{}); !strings.Contains(view.Text, "сразу после оплаты") {
		t.Fatalf("a pending gift must say it becomes redeemable once paid: %s", view.Text)
	}
}
