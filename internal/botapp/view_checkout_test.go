package botapp

import (
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

func TestChangeMethodKeepsTheCheckout(t *testing.T) {
	t.Parallel()
	session := CheckoutSession{PlanVersionID: "a", Operation: "extension", Provider: "yookassa", SubscriptionID: "sub-2"}
	quote := commerce.CheckoutQuote{Subtotal: commerce.Money{Amount: 1000, Currency: "RUB"}, ExternalMinor: 1000}
	view := checkoutView(LocaleEnglish, Plan{Name: "Pro", Duration: 24 * time.Hour}, session, quote, false)
	callbacks := strings.Join(callbackData(view), " ")
	if strings.Contains(callbacks, actionPrefix+"buy:") {
		t.Fatalf("changing the method must not reopen the checkout through the plan page: %s", callbacks)
	}
	if !strings.Contains(callbacks, actionPrefix+"pm-change") {
		t.Fatalf("the summary must offer to change the method in place: %s", callbacks)
	}
	if !expansionActions["pm-change"] || !commerceActions["pm-change"] {
		t.Fatal("pm-change must be a routable commerce action")
	}
}

func TestPaymentMethodViewReturnsWhereItCameFrom(t *testing.T) {
	t.Parallel()
	choices := []PaymentChoice{{Provider: "yookassa", Currency: "RUB", AmountMinor: 1000}}
	fromPlan := paymentMethodView(LocaleEnglish, Plan{PlanVersionID: "pv"}, choices, "")
	if !strings.Contains(strings.Join(callbackData(fromPlan), " "), actionPrefix+"plan:pv") {
		t.Fatalf("the first method choice backs out to the plan: %v", callbackData(fromPlan))
	}
	fromSummary := paymentMethodView(LocaleEnglish, Plan{PlanVersionID: "pv"}, choices, "checkout")
	if !strings.Contains(strings.Join(callbackData(fromSummary), " "), actionPrefix+"checkout") {
		t.Fatalf("changing the method backs out to the summary: %v", callbackData(fromSummary))
	}
	if strings.Contains(strings.Join(callbackData(fromSummary), " "), actionPrefix+"plan:") {
		t.Fatalf("backing out of a change must not leave the checkout: %v", callbackData(fromSummary))
	}
}
