//go:build integration

package integrationtest

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/omniflow/omniflow/internal/selfemployed"
)

// fakeMoiNalog records incomes and can fail the next create with a 502 after
// registering it, which is the timeout-after-success case the dedupe exists for.
type fakeMoiNalog struct {
	mu            sync.Mutex
	created       int
	cancelled     int
	failAfterSave bool
	incomes       []map[string]any
}

func (fake *fakeMoiNalog) handler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		switch request.URL.Path {
		case "/api/v1/auth/lkfl", "/api/v1/auth/token":
			_, _ = writer.Write([]byte(`{"token":"t","refreshToken":"r","tokenExpireIn":"2099-01-01T00:00:00Z"}`))
		case "/api/v1/income":
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			fake.created++
			body["approvedReceiptUuid"] = "uuid-" + string(rune('a'+fake.created))
			fake.incomes = append(fake.incomes, body)
			if fake.failAfterSave {
				fake.failAfterSave = false
				writer.WriteHeader(http.StatusBadGateway)
				return
			}
			_, _ = writer.Write([]byte(`{"approvedReceiptUuid":"` + body["approvedReceiptUuid"].(string) + `"}`))
		case "/api/v1/incomes":
			content := []map[string]any{}
			for _, income := range fake.incomes {
				content = append(content, map[string]any{
					"approvedReceiptUuid": income["approvedReceiptUuid"], "totalAmount": income["totalAmount"],
					"operationTime": income["operationTime"],
				})
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"content": content})
		case "/api/v1/cancel":
			fake.cancelled++
			_, _ = writer.Write([]byte(`{}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	})
}

func (harness *harness) paidRubleOrder(ctx context.Context, t *testing.T, provider string, amount int64) string {
	t.Helper()
	customer := harness.customer(ctx, t)
	var orderID string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO orders (user_id, state, operation, currency, subtotal_minor,
			external_minor, paid_minor, idempotency_key, paid_at)
		VALUES ($1::uuid, 'paid', 'purchase', 'RUB', $2, $2, $2, 'npd-' || $1, now()) RETURNING id::text`,
		customer, amount).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.pool.Exec(ctx, `INSERT INTO payment_intents (order_id, provider, status, amount_minor, currency,
			provider_reference, idempotency_key)
		VALUES ($1::uuid, $2, 'succeeded', $3, 'RUB', 'ref-' || $1, 'intent-' || $1)`, orderID, provider, amount); err != nil {
		t.Fatal(err)
	}
	return orderID
}

// A settled ruble payment gets exactly one receipt even when the first
// attempt timed out after the tax service had registered it; a provider the
// operator excluded gets none; a refund cancels the receipt.
func TestSelfEmployedReceiptsAreIssuedOnce(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	fake := &fakeMoiNalog{failAfterSave: true}
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)
	service, err := selfemployed.New(harness.pool, selfemployed.Options{
		DataKey: operationsKey, BaseURL: server.URL + "/api", Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	actor := harness.operator(ctx, t, "npd@omniflow.test")
	before := harness.paidRubleOrder(ctx, t, "yookassa", 10000)
	if _, err = service.SaveSettings(ctx, selfemployed.SettingsInput{Enabled: true, INN: "123456789012"}, actor); err == nil {
		t.Fatal("enabled without a password")
	}
	if _, err = service.SaveSettings(ctx, selfemployed.SettingsInput{INN: "12345"}, actor); err == nil {
		t.Fatal("a malformed INN was accepted")
	}
	if _, err = harness.pool.Exec(ctx, `UPDATE orders SET paid_at = now() - interval '1 hour' WHERE id = $1::uuid`, before); err != nil {
		t.Fatal(err)
	}
	settings, err := service.SaveSettings(ctx, selfemployed.SettingsInput{
		Enabled: true, INN: "123456789012", Password: "secret", Providers: []string{"yookassa"},
	}, actor)
	if err != nil || !settings.Enabled || !settings.HasPassword {
		t.Fatalf("settings = %+v (%v)", settings, err)
	}
	if err = service.CheckConnection(ctx, actor); err != nil {
		t.Fatal(err)
	}
	order := harness.paidRubleOrder(ctx, t, "yookassa", 14950)
	excluded := harness.paidRubleOrder(ctx, t, "cryptobot", 5000)

	// First pass: the create answers 502 after saving, so the receipt is uncertain.
	if err = service.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	receipts, err := service.List(ctx, "", 10)
	if err != nil || len(receipts) != 1 || receipts[0].Status != "uncertain" || receipts[0].OrderID != order {
		t.Fatalf("after the first pass = %+v (%v)", receipts, err)
	}
	if _, err = harness.pool.Exec(ctx, `UPDATE self_employed_receipts SET next_attempt_at = now()`); err != nil {
		t.Fatal(err)
	}
	// Second pass: the lookup finds the receipt; nothing is created again.
	if err = service.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	receipts, _ = service.List(ctx, "", 10)
	if receipts[0].Status != "issued" || receipts[0].ReceiptURL == "" || fake.created != 1 {
		t.Fatalf("after the lookup = %+v, created %d", receipts, fake.created)
	}
	if url, err := service.CustomerReceiptURL(ctx, "00000000-0000-4000-8000-000000000000", order); err != nil || url != "" {
		t.Fatalf("a stranger saw a receipt: %q %v", url, err)
	}
	var notices int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM operator_notifications WHERE kind = 'receipt'`).Scan(&notices); err != nil || notices != 1 {
		t.Fatalf("receipt notices = %d (%v)", notices, err)
	}
	for _, other := range []string{before, excluded} {
		var count int
		if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM self_employed_receipts WHERE order_id = $1::uuid`, other).Scan(&count); err != nil || count != 0 {
			t.Fatalf("order %s got a receipt", other)
		}
	}

	if _, err = harness.pool.Exec(ctx, `UPDATE orders SET state = 'refunded', refunded_minor = paid_minor WHERE id = $1::uuid`, order); err != nil {
		t.Fatal(err)
	}
	if err = service.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	receipts, _ = service.List(ctx, "", 10)
	if receipts[0].Status != "cancelled" || fake.cancelled != 1 {
		t.Fatalf("after the refund = %+v, cancelled %d", receipts, fake.cancelled)
	}
}
