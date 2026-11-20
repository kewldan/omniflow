package payments

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

func TestYooKassaSBPButtonAndTestMode(t *testing.T) {
	t.Parallel()
	var body map[string]any
	test := true
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			body = nil
			_ = json.NewDecoder(request.Body).Decode(&body)
		}
		flag := "false"
		if test {
			flag = "true"
		}
		_, _ = io.WriteString(writer, `{"id":"p1","status":"succeeded","test":`+flag+`,"amount":{"value":"1.00","currency":"RUB"}}`)
	}))
	t.Cleanup(server.Close)
	provider, _ := NewYooKassa("shop", "secret")
	provider.baseURL = server.URL
	if len(ProviderMethods(provider)) != 1 {
		t.Fatal("SBP is offered while switched off")
	}
	provider.WithModes(true, false)
	if methods := ProviderMethods(provider); len(methods) != 2 || methods[1] != "sbp" {
		t.Fatalf("methods = %v", methods)
	}
	amount := commerce.Money{Amount: 100, Currency: "RUB"}
	if _, err := provider.Create(context.Background(), CreateRequest{OrderID: "o", IdempotencyKey: "key-00000001", Amount: amount, Method: "sbp"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := body["payment_method_data"].(map[string]any); data["type"] != "sbp" {
		t.Fatalf("the SBP button did not ask for SBP: %#v", body)
	}
	if _, err := provider.Create(context.Background(), CreateRequest{OrderID: "o", IdempotencyKey: "key-00000002", Amount: amount}); err != nil {
		t.Fatal(err)
	}
	if _, asked := body["payment_method_data"]; asked {
		t.Fatal("the default button forced a method")
	}

	settled, _ := provider.Poll(context.Background(), "p1")
	if settled.Status != "failed" {
		t.Fatalf("a test payment outside test mode settled as %q", settled.Status)
	}
	provider.WithModes(true, true)
	if settled, _ = provider.Poll(context.Background(), "p1"); settled.Status != "succeeded" {
		t.Fatalf("a test payment in test mode is %q", settled.Status)
	}
	test = false
	provider.WithModes(true, false)
	if settled, _ = provider.Poll(context.Background(), "p1"); settled.Status != "succeeded" {
		t.Fatalf("a live payment is %q", settled.Status)
	}
}

func TestCryptoBotAssetsAndLifetime(t *testing.T) {
	t.Parallel()
	env := func(values map[string]string) Settings {
		return Settings{Env: func(key string) string { return values[key] }}
	}
	assets, lifetime, err := CryptoBotOptionsFromEnv(env(map[string]string{"APP_CRYPTOBOT_ASSETS": "usdt, TON", "APP_CRYPTOBOT_INVOICE_EXPIRES_HOURS": "24"}))
	if err != nil || len(assets) != 2 || assets[0] != "USDT" || lifetime != 24*time.Hour {
		t.Fatalf("%v %v %v", assets, lifetime, err)
	}
	for _, bad := range []map[string]string{{"APP_CRYPTOBOT_ASSETS": "U$DT"}, {"APP_CRYPTOBOT_INVOICE_EXPIRES_HOURS": "0"}, {"APP_CRYPTOBOT_INVOICE_EXPIRES_HOURS": "745"}} {
		if _, _, err := CryptoBotOptionsFromEnv(env(bad)); err == nil {
			t.Fatalf("%v was accepted", bad)
		}
	}
	var form url.Values
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = request.ParseForm()
		form = request.PostForm
		if len(form) == 0 {
			raw, _ := io.ReadAll(request.Body)
			form, _ = url.ParseQuery(string(raw))
		}
		_, _ = io.WriteString(writer, `{"ok":true,"result":{"invoice_id":1,"status":"active","bot_invoice_url":"https://t.me/x"}}`)
	}))
	t.Cleanup(server.Close)
	provider, _ := NewCryptoBot("token", false)
	provider.baseURL = server.URL
	provider.WithInvoiceOptions(assets, lifetime)
	_, _ = provider.Create(context.Background(), CreateRequest{OrderID: "o", Amount: commerce.Money{Amount: 100, Currency: "USD"}})
	if form.Get("expires_in") != "86400" || form.Get("accepted_assets") != "USDT,TON" {
		t.Fatalf("form = %v", form)
	}
}
