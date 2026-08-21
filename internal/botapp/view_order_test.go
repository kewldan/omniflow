package botapp

import (
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
)

func TestPendingOrderWithoutAPaymentOffersToStartOne(t *testing.T) {
	t.Parallel()
	order := OrderSummary{ID: "abcdef12-0000-0000-0000-000000000000", State: commerce.OrderPending, Currency: "RUB", ExternalMinor: 34900, Phase: commerce.PaymentPhaseAwaitingAction}
	view := orderStatusView(LocaleEnglish, order, nil)
	callbacks := strings.Join(callbackData(view), " ")
	if !strings.Contains(callbacks, actionPrefix+"pay:"+order.ID) {
		t.Fatalf("an order with no payment must offer to start one: %s", callbacks)
	}
	// A provider that refused the first attempt leaves a processing intent
	// with no page to open; that is still nothing to tap.
	refused := order
	refused.Provider, refused.PaymentIntentID, refused.PaymentStatus, refused.Phase = "yookassa", "pi", "processing", commerce.PaymentPhasePending
	if !strings.Contains(strings.Join(callbackData(orderStatusView(LocaleEnglish, refused, nil)), " "), actionPrefix+"pay:") {
		t.Fatal("a refused payment start must still offer to start the payment")
	}
	for name, ready := range map[string]OrderSummary{
		"hosted page": {ID: order.ID, State: commerce.OrderPending, Currency: "RUB", ExternalMinor: 100, Phase: commerce.PaymentPhasePending, Provider: "yookassa", PaymentIntentID: "pi", CheckoutURL: "https://pay.example/x"},
		"stars":       {ID: order.ID, State: commerce.OrderPending, Currency: "XTR", ExternalMinor: 100, Phase: commerce.PaymentPhaseAwaitingAction, Provider: "telegram_stars", PaymentIntentID: "pi"},
		"manual":      {ID: order.ID, State: commerce.OrderPending, Currency: "RUB", ExternalMinor: 100, Phase: commerce.PaymentPhasePending, Provider: "manual", PaymentIntentID: "pi", PaymentStatus: "requires_action"},
	} {
		if strings.Contains(strings.Join(callbackData(orderStatusView(LocaleEnglish, ready, nil)), " "), actionPrefix+"pay:") {
			t.Fatalf("%s: the order already has a way to pay and must not offer a second start", name)
		}
	}
}

func TestFailedPaymentOnAnOpenOrderOffersAnotherMethod(t *testing.T) {
	t.Parallel()
	order := OrderSummary{ID: "abcdef12-0000-0000-0000-000000000000", State: commerce.OrderPending, Currency: "RUB", ExternalMinor: 100, Phase: commerce.PaymentPhaseFailed, Provider: "cryptobot", PaymentStatus: "failed"}
	view := orderStatusView(LocaleEnglish, order, nil)
	callbacks := strings.Join(callbackData(view), " ")
	if !strings.Contains(callbacks, actionPrefix+"pay:"+order.ID+":pick") {
		t.Fatalf("a failed payment on an open order must offer another method: %s", callbacks)
	}
	closed := order
	closed.State = commerce.OrderCancelled
	if strings.Contains(strings.Join(callbackData(orderStatusView(LocaleEnglish, closed, nil)), " "), actionPrefix+"pay:") {
		t.Fatal("a closed order cannot be paid")
	}
}

func TestOrderPaymentMethodViewFitsTelegramCallbackLimit(t *testing.T) {
	t.Parallel()
	order := OrderSummary{ID: "abcdef12-3456-7890-abcd-ef1234567890", Currency: "XTR", ExternalMinor: 250}
	view := orderPaymentMethodView(LocaleEnglish, order, []PaymentChoice{
		{Provider: "telegram_stars", Currency: "XTR", AmountMinor: 250},
		{Provider: "cryptobot", Currency: "XTR", AmountMinor: 250},
	})
	for _, data := range callbackData(view) {
		if len(data) > 64 {
			t.Fatalf("Telegram refuses callback data over 64 bytes: %q is %d", data, len(data))
		}
	}
	labels := buttonLabels(view)
	if !strings.Contains(labels, "⭐ 250") || !strings.Contains(labels, "CryptoBot") {
		t.Fatalf("each method must be priced at what the order owes: %s", labels)
	}
	if !strings.Contains(strings.Join(callbackData(view), " "), actionPrefix+"order-pm:"+order.ID+":telegram_stars") {
		t.Fatalf("the method callback must name the order and the provider: %v", callbackData(view))
	}
	empty := orderPaymentMethodView(LocaleRussian, order, nil)
	if !strings.Contains(empty.Text, "Нет доступных способов оплаты") {
		t.Fatalf("no method must be explained: %s", empty.Text)
	}
}

func TestPaymentStartFailedViewLeadsBackToThePayment(t *testing.T) {
	t.Parallel()
	view := paymentStartFailedView(LocaleEnglish, "abcdef12-0000-0000-0000-000000000000")
	callbacks := callbackData(view)
	if callbacks[0] != actionPrefix+"pay:abcdef12-0000-0000-0000-000000000000" {
		t.Fatalf("try again must restart the payment: %v", callbacks)
	}
	if callbacks[1] != actionPrefix+"pay:abcdef12-0000-0000-0000-000000000000:pick" {
		t.Fatalf("another method must be offered: %v", callbacks)
	}
}

func TestPaymentIntentDeadOnlyForTerminalStatuses(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"failed", "cancelled", "expired"} {
		if !paymentIntentDead(status) {
			t.Fatalf("%s is terminal", status)
		}
	}
	for _, status := range []string{"", "pending", "requires_action", "processing", "succeeded"} {
		if paymentIntentDead(status) {
			t.Fatalf("%s is not terminal", status)
		}
	}
}
