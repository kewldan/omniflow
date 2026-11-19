package payments

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
)

func TestYooKassaReceiptFromEnv(t *testing.T) {
	t.Parallel()
	env := func(values map[string]string) Settings {
		return Settings{Env: func(key string) string { return values[key] }}
	}
	off, err := YooKassaReceiptFromEnv(env(nil))
	if err != nil || off.Enabled {
		t.Fatalf("no settings: %+v %v, want receipts off", off, err)
	}
	on, err := YooKassaReceiptFromEnv(env(map[string]string{"APP_YOOKASSA_RECEIPT_EMAIL": "receipts@example.com"}))
	if err != nil || !on.Enabled || on.VATCode != 1 || on.PaymentSubject != "service" || on.PaymentMode != "full_payment" {
		t.Fatalf("default e-mail: %+v %v, want receipts on with Bedolaga's defaults", on, err)
	}
	if _, err := YooKassaReceiptFromEnv(env(map[string]string{"APP_YOOKASSA_RECEIPTS": "true"})); err == nil {
		t.Fatal("receipts without a contact were accepted")
	}
	if _, err := YooKassaReceiptFromEnv(env(map[string]string{"APP_YOOKASSA_RECEIPT_EMAIL": "a@b.c", "APP_YOOKASSA_VAT_CODE": "99"})); err == nil {
		t.Fatal("an unknown vat_code was accepted")
	}
}

func TestYooKassaSendsTheReceiptWithEveryPayment(t *testing.T) {
	t.Parallel()
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		bodies = append(bodies, body)
		_, _ = writer.Write([]byte(`{"id":"p1","status":"pending","amount":{"value":"229.00","currency":"RUB"},"confirmation":{"confirmation_url":"https://pay.test"}}`))
	}))
	t.Cleanup(server.Close)
	provider, err := NewYooKassa("shop", "secret")
	if err != nil {
		t.Fatal(err)
	}
	provider.baseURL = server.URL
	provider.WithReceipt(YooKassaReceipt{Enabled: true, DefaultEmail: "receipts@example.com", VATCode: 1, PaymentSubject: "service", PaymentMode: "full_payment"})

	long := strings.Repeat("ж", 200)
	if _, err := provider.Create(context.Background(), CreateRequest{OrderID: "o1", IdempotencyKey: "key-00000001", Amount: commerce.Money{Amount: 22900, Currency: "RUB"}, Description: long}); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ChargeSaved(context.Background(), SavedChargeRequest{OrderID: "o2", IdempotencyKey: "key-00000002", MethodToken: "pm", Amount: commerce.Money{Amount: 22900, Currency: "RUB"}, Description: "Продление"}); err != nil {
		t.Fatal(err)
	}
	for index, body := range bodies {
		receipt, ok := body["receipt"].(map[string]any)
		if !ok {
			t.Fatalf("request %d carries no receipt: %v", index, body)
		}
		if receipt["customer"].(map[string]any)["email"] != "receipts@example.com" {
			t.Fatalf("request %d receipt contact = %v", index, receipt["customer"])
		}
		item := receipt["items"].([]any)[0].(map[string]any)
		amount := item["amount"].(map[string]any)
		if amount["value"] != "229.00" || amount["currency"] != "RUB" || item["vat_code"].(float64) != 1 ||
			item["payment_subject"] != "service" || item["payment_mode"] != "full_payment" || item["quantity"] != "1.00" {
			t.Fatalf("request %d receipt item = %v", index, item)
		}
		if len([]rune(item["description"].(string))) > 128 {
			t.Fatalf("request %d description exceeds 128 characters", index)
		}
	}
}

func TestYooKassaSendsNoReceiptWhenOff(t *testing.T) {
	t.Parallel()
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = json.NewDecoder(request.Body).Decode(&body)
		_, _ = writer.Write([]byte(`{"id":"p1","status":"pending","amount":{"value":"1.00","currency":"RUB"}}`))
	}))
	t.Cleanup(server.Close)
	provider, _ := NewYooKassa("shop", "secret")
	provider.baseURL = server.URL
	if _, err := provider.Create(context.Background(), CreateRequest{OrderID: "o", IdempotencyKey: "key-00000003", Amount: commerce.Money{Amount: 100, Currency: "RUB"}}); err != nil {
		t.Fatal(err)
	}
	if _, present := body["receipt"]; present {
		t.Fatal("a receipt was sent with receipts off")
	}
}
