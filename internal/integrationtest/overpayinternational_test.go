//go:build integration

package integrationtest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/payments"
	"github.com/omniflow/omniflow/internal/paymentservice"
)

// fakeOverpay is Overpay's order API: each checkout gets the next order
// number, and a poll reports the order charged with whatever euro amount the
// test set for it.
type fakeOverpay struct {
	mutex   sync.Mutex
	opened  int
	charged map[string]string
}

func (fake *fakeOverpay) charge(id, amount string) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.charged[id] = amount
}

func (fake *fakeOverpay) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	if request.Method == http.MethodPost {
		fake.opened++
		id := "op-int-" + strconv.Itoa(fake.opened)
		_, _ = writer.Write([]byte(`{"id":"` + id + `","resultUrl":"https://pay.example/` + id + `"}`))
		return
	}
	id := strings.TrimPrefix(request.URL.Path, "/orders/")
	_, _ = writer.Write([]byte(`{"id":"` + id + `","status":"charged","amount":"` + fake.charged[id] + `","currency":"EUR"}`))
}

func overpayInternationalProvider(t *testing.T, server string) payments.Provider {
	t.Helper()
	values := map[string]string{
		"APP_OVERPAY_USERNAME": "merchant", "APP_OVERPAY_PASSWORD": "secret",
		"APP_OVERPAY_PROJECT_ID": "project-rub", "APP_OVERPAY_API_URL": server,
		"APP_OVERPAY_INT_PROJECT_ID": "project-int", "APP_OVERPAY_INT_RATE": "98.50",
	}
	for _, builder := range payments.Builders {
		if builder.Slug == "overpay" {
			provider, err := builder.Build(payments.Settings{Env: func(key string) string { return values[key] }})
			if err != nil {
				t.Fatalf("build overpay: %v", err)
			}
			return provider
		}
	}
	t.Fatal("overpay is not registered")
	return nil
}

// TestOverpayForeignCardSettlesTheRubleOrderThroughItsQuote is the foreign-card
// terminal end to end: a ruble order opened on the euro terminal keeps the
// quote it was opened at, and the euros Overpay later reports are mapped back
// onto the order through that quote — exactly when the customer paid what was
// asked, and as an underpayment when they paid less.
func TestOverpayForeignCardSettlesTheRubleOrderThroughItsQuote(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	store := harness.commerceStore(t, testOptions())
	planVersionID := harness.catalog(ctx, t, "overpay-int-plan", 59900)
	fake := &fakeOverpay{charged: map[string]string{}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	service := paymentservice.New(harness.pool, store, overpayInternationalProvider(t, server.URL))

	open := func(key string) (string, string, string) {
		customerID := harness.customer(ctx, t)
		order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
			CustomerID: customerID, PlanVersionID: planVersionID, Currency: "RUB",
			Operation: "purchase", IdempotencyKey: key,
		})
		if err != nil {
			t.Fatalf("create order: %v", err)
		}
		intent, err := service.CreateIntent(ctx, paymentservice.CreateIntentInput{
			OrderID: uuidText(order.ID), Provider: "overpay", IdempotencyKey: key + ":intl", Method: "intl",
		})
		if err != nil {
			t.Fatalf("open the euro checkout: %v", err)
		}
		return uuidText(order.ID), uuidText(intent.ID), intent.ProviderReference.String
	}
	notify := func(reference, orderID string) (string, error) {
		body := []byte(`{"id":"` + reference + `","status":"charged","merchantTransactionId":"` + orderID + `"}`)
		return service.HandleWebhook(ctx, "overpay", http.Header{}, body)
	}

	orderID, intentID, reference := open("overpay-int-exact")
	var amount, charged int64
	var currency, chargedCurrency string
	if err := harness.pool.QueryRow(ctx, `SELECT amount_minor, currency, charged_minor, charged_currency
		FROM payment_quotes WHERE payment_intent_id = $1::uuid`, intentID).Scan(&amount, &currency, &charged, &chargedCurrency); err != nil {
		t.Fatalf("the quote must be kept when the checkout opens: %v", err)
	}
	// 599.00 RUB at 98.50 a euro, rounded up to the cent.
	if amount != 59900 || currency != "RUB" || charged != 609 || chargedCurrency != "EUR" {
		t.Fatalf("quote = %d %s for %d %s", amount, currency, charged, chargedCurrency)
	}

	// Opening the same checkout again is the same intent and the same quote,
	// not a second euro order at whatever the rate is by then.
	again, err := service.CreateIntent(ctx, paymentservice.CreateIntentInput{
		OrderID: orderID, Provider: "overpay", IdempotencyKey: "overpay-int-exact:intl", Method: "intl",
	})
	if err != nil || uuidText(again.ID) != intentID || again.ProviderReference.String != reference {
		t.Fatalf("a repeated checkout must return the first intent: %v", err)
	}
	var quotes int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM payment_quotes`).Scan(&quotes); err != nil || quotes != 1 {
		t.Fatalf("%d quotes after a repeated checkout, want 1 (%v)", quotes, err)
	}

	fake.charge(reference, "6.09")
	classification, err := notify(reference, orderID)
	if err != nil || classification != commerce.ClassificationPaid {
		t.Fatalf("exact euro payment: %q %v", classification, err)
	}
	var state string
	var paid int64
	if err := harness.pool.QueryRow(ctx, `SELECT state, paid_minor FROM orders WHERE id = $1::uuid`, orderID).Scan(&state, &paid); err != nil {
		t.Fatal(err)
	}
	if state != "paid" || paid != 59900 {
		t.Fatalf("order %s paid %d, want paid 59900 rubles", state, paid)
	}

	// A second order paid a euro short is converted at the quote's ratio and
	// is an underpayment, not a sale.
	orderID, _, reference = open("overpay-int-short")
	fake.charge(reference, "5.09")
	classification, err = notify(reference, orderID)
	if classification != commerce.ClassificationUnderpayment {
		t.Fatalf("a short euro payment must be an underpayment, got %q %v", classification, err)
	}
}
