package nalogo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientAuthenticatesAndRegistersIncome(t *testing.T) {
	t.Parallel()
	var income map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/auth/lkfl":
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["username"] != "123456789012" || body["password"] != "secret" {
				writer.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			_, _ = writer.Write([]byte(`{"token":"t1","refreshToken":"r1","tokenExpireIn":"2099-01-01T00:00:00Z"}`))
		case "/api/v1/income":
			if request.Header.Get("Authorization") != "Bearer t1" {
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewDecoder(request.Body).Decode(&income)
			_, _ = writer.Write([]byte(`{"approvedReceiptUuid":"200abc"}`))
		case "/api/v1/incomes":
			_, _ = writer.Write([]byte(`{"content":[{"approvedReceiptUuid":"200old","totalAmount":149.5,"operationTime":"2026-11-20T15:00:00+03:00"}]}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client := New(server.URL+"/api", "", nil)

	if _, err := client.Authenticate(context.Background(), "123456789012", "wrong"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong password: %v", err)
	}
	token, err := client.Authenticate(context.Background(), "123456789012", "secret")
	if err != nil || token.Access != "t1" || !token.Valid(time.Now()) {
		t.Fatalf("token = %+v (%v)", token, err)
	}
	operation := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	receipt, err := client.CreateIncome(context.Background(), token.Access,
		Income{Name: "VPN", AmountMinor: 14950, OperationTime: operation})
	if err != nil || receipt != "200abc" {
		t.Fatalf("receipt = %q (%v)", receipt, err)
	}
	if income["totalAmount"] != "149.50" || income["operationTime"] != "2026-11-20T15:00:00+03:00" ||
		income["paymentType"] != "CASH" {
		t.Fatalf("income body = %v", income)
	}
	if _, err = client.CreateIncome(context.Background(), "stale", Income{AmountMinor: 100}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("stale token: %v", err)
	}
	found, ok, err := client.FindIncome(context.Background(), token.Access,
		Income{AmountMinor: 14950, OperationTime: operation.Add(3 * time.Minute)})
	if err != nil || !ok || found != "200old" {
		t.Fatalf("find = %q %v %v", found, ok, err)
	}
	if _, ok, _ = client.FindIncome(context.Background(), token.Access, Income{AmountMinor: 100, OperationTime: operation}); ok {
		t.Fatal("a different amount matched")
	}
	if url := PrintURL("", "123456789012", "200abc"); !strings.HasSuffix(url, "/v1/receipt/123456789012/200abc/print") {
		t.Fatalf("print url = %s", url)
	}
}

func TestServerErrorsAreUncertain(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/income") {
			writer.WriteHeader(http.StatusBadGateway)
			return
		}
		writer.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)
	client := New(server.URL, "dev", nil)
	if _, err := client.CreateIncome(context.Background(), "t", Income{AmountMinor: 100}); !errors.Is(err, ErrUncertain) {
		t.Fatalf("5xx: %v", err)
	}
	if err := client.CancelIncome(context.Background(), "t", "x"); !errors.Is(err, ErrRejected) {
		t.Fatalf("4xx: %v", err)
	}
	if rubles(5) != "0.05" || rubles(100000) != "1000.00" {
		t.Fatal("rubles formatting")
	}
}
