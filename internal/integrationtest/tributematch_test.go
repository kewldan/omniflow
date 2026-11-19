//go:build integration

package integrationtest

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"testing"

	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/payments"
	"github.com/omniflow/omniflow/internal/paymentservice"
)

func tributeProvider(t *testing.T) payments.Provider {
	t.Helper()
	env := map[string]string{"APP_TRIBUTE_API_KEY": "tribute-secret", "APP_TRIBUTE_DONATE_LINK": "https://t.me/tribute/app?startapp=dXYZ"}
	for _, builder := range payments.Builders {
		if builder.Slug == "tribute" {
			provider, err := builder.Build(payments.Settings{Env: func(key string) string { return env[key] }})
			if err != nil {
				t.Fatalf("build tribute: %v", err)
			}
			return provider
		}
	}
	t.Fatal("tribute builder is not registered")
	return nil
}

func signedTribute(body string) http.Header {
	mac := hmac.New(sha256.New, []byte("tribute-secret"))
	mac.Write([]byte(body))
	headers := http.Header{}
	headers.Set("trbt-signature", hex.EncodeToString(mac.Sum(nil)))
	return headers
}

// A Tribute donation names the payer, not the order. It settles the payer's
// open Tribute intent for exactly that amount, once, and a donation from
// somebody with no such intent is refused rather than credited at random.
func TestTributeDonationSettlesThePayersOpenIntentOnce(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	service := paymentservice.New(harness.pool, store, tributeProvider(t))

	customerID := harness.customer(ctx, t)
	if _, err := harness.pool.Exec(ctx, `INSERT INTO identities (user_id, provider, provider_subject, status)
		VALUES ($1::uuid, 'telegram', '424242', 'active')`, customerID); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	checkout, err := accountcheckout.New(harness.pool, store, service, accountcheckout.Options{
		Logger:   slog.New(slog.DiscardHandler),
		Settings: accountcheckout.Settings{Currency: "RUB", PublicURL: "https://example.test"},
	})
	if err != nil {
		t.Fatalf("build checkout: %v", err)
	}
	topUp, err := checkout.StartTopUp(ctx, customerID, "RUB", 10000, "tribute", "tribute-topup-0001")
	if err != nil {
		t.Fatalf("start top-up: %v", err)
	}
	orderID := topUp.OrderID

	stranger := `{"name":"new_donation","created_at":"2026-11-19T10:00:00Z","payload":{"donation_request_id":7,"amount":10000,"currency":"rub","telegram_user_id":999}}`
	if _, err := service.HandleWebhook(ctx, "tribute", signedTribute(stranger), []byte(stranger)); err == nil {
		t.Fatal("a donation from a payer with no open intent was applied")
	}

	donation := `{"name":"new_donation","created_at":"2026-11-19T10:05:00Z","payload":{"donation_request_id":7,"amount":10000,"currency":"rub","telegram_user_id":424242}}`
	if _, err := service.HandleWebhook(ctx, "tribute", signedTribute(donation), []byte(donation)); err != nil {
		t.Fatalf("matching donation: %v", err)
	}
	again, err := service.HandleWebhook(ctx, "tribute", signedTribute(donation), []byte(donation))
	if err != nil || again != "duplicate" {
		t.Fatalf("redelivery = %q, %v; want duplicate", again, err)
	}
	var paid int64
	if err := harness.pool.QueryRow(ctx, `SELECT paid_minor FROM orders WHERE id = $1::uuid`, orderID).Scan(&paid); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if paid != 10000 {
		t.Fatalf("order paid %d, want 10000 exactly once", paid)
	}
}
