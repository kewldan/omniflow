package payments

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
)

// YooKassa asks to keep the card only on consent, and reports the saved
// payment method's token and masked label only once the payment succeeded.
func TestYooKassaBindsACardOnlyWithConsent(t *testing.T) {
	t.Parallel()
	var bodies []map[string]any
	status := "pending"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		bodies = append(bodies, body)
		_, _ = io.WriteString(writer, `{"id":"p1","status":"`+status+`","amount":{"value":"229.00","currency":"RUB"},
			"confirmation":{"confirmation_url":"https://pay.test"},
			"payment_method":{"type":"bank_card","id":"pm-1","saved":true,"card":{"last4":"4242","card_type":"Visa"}}}`)
	}))
	t.Cleanup(server.Close)
	provider, err := NewYooKassa("shop", "secret")
	if err != nil {
		t.Fatal(err)
	}
	provider.baseURL = server.URL
	amount := commerce.Money{Amount: 22900, Currency: "RUB"}

	if _, err = provider.Create(context.Background(), CreateRequest{OrderID: "o1", IdempotencyKey: "key-00000001", Amount: amount}); err != nil {
		t.Fatal(err)
	}
	if _, asked := bodies[0]["save_payment_method"]; asked {
		t.Fatal("a checkout without consent asked to save the card")
	}
	intent, err := provider.Create(context.Background(), CreateRequest{OrderID: "o2", IdempotencyKey: "key-00000002", Amount: amount, SavePaymentMethod: true})
	if err != nil {
		t.Fatal(err)
	}
	if bodies[1]["save_payment_method"] != true {
		t.Fatalf("consent was not sent: %#v", bodies[1])
	}
	if intent.BoundMethod != nil {
		t.Fatal("a pending payment reported a bound method")
	}

	status = "succeeded"
	settled, err := provider.Poll(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if settled.BoundMethod == nil || settled.BoundMethod.Token != "pm-1" || settled.BoundMethod.Label != "Visa *4242" {
		t.Fatalf("bound method = %#v", settled.BoundMethod)
	}
}

// A YooKassa method the bank did not allow to be saved is not a binding.
func TestYooKassaIgnoresAnUnsavedMethod(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"id":"p1","status":"succeeded","amount":{"value":"1.00","currency":"RUB"},
			"payment_method":{"type":"sbp","id":"pm-2","saved":false}}`)
	}))
	t.Cleanup(server.Close)
	provider, _ := NewYooKassa("shop", "secret")
	provider.baseURL = server.URL
	intent, err := provider.Poll(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if intent.BoundMethod != nil {
		t.Fatalf("an unsaved method was reported as bound: %#v", intent.BoundMethod)
	}
}

// HOT Pay enrols a mandate only on consent and only for a stable customer
// identifier, and a settled payment with an active enrolment names it.
func TestPayHotEnrolsAMandateOnlyWithConsent(t *testing.T) {
	var body map[string]any
	enrolment := ""
	provider := payhotAt(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			body = nil
			_ = json.NewDecoder(request.Body).Decode(&body)
			writer.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(writer, `{"payment":`+payhotPaymentJSON("requires_payment_method")+`,"checkout":{"url":"https://checkout.pay.hot/s/abc"}}`)
			return
		}
		payment := strings.TrimSuffix(payhotPaymentJSON("succeeded"), "}") + enrolment + "}"
		_, _ = io.WriteString(writer, `{"payment":`+payment+`}`)
	})
	customer := "9b6f1c2e-0000-4000-8000-000000000001"
	request := CreateRequest{
		OrderID: payhotTestOrder, IdempotencyKey: "checkout-key-000001", Amount: commerce.Money{Amount: 12345, Currency: "RUB"},
		Metadata: map[string]string{"customer_id": customer},
	}
	if _, err := provider.Create(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if _, asked := body["recurring_enrollment"]; asked {
		t.Fatal("a checkout without consent asked for a mandate")
	}
	request.SavePaymentMethod = true
	if _, err := provider.Create(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if consent, ok := body["recurring_enrollment"].(map[string]any); !ok || len(consent) != 0 {
		t.Fatalf("consent marker = %#v", body["recurring_enrollment"])
	}
	if who, _ := body["customer"].(map[string]any); who["id"] != customer {
		t.Fatalf("a mandate must belong to the customer, got %#v", body["customer"])
	}

	enrolment = `,"recurring_enrollment":{"mandate_id":"` + payhotTestMandate + `","status":"active"}`
	settled, err := provider.Poll(t.Context(), payhotTestPayment)
	if err != nil {
		t.Fatal(err)
	}
	if settled.BoundMethod == nil || settled.BoundMethod.Token != payhotTestMandate {
		t.Fatalf("bound method = %#v", settled.BoundMethod)
	}
	enrolment = `,"recurring_enrollment":{"mandate_id":"` + payhotTestMandate + `","status":"failed"}`
	if failed, _ := provider.Poll(t.Context(), payhotTestPayment); failed.BoundMethod != nil {
		t.Fatal("a failed enrolment was reported as bound")
	}
}
