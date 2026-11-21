//go:build integration

package integrationtest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/operator"
)

// A notice is rendered with what the database says about the records it
// names: the customer's Telegram handle and how many times they paid, the
// plan and the period an order bought, the provider and method that settled
// it, the wallet after a top-up, and a ticket's queue.
func TestOperatorNoticesReadTheirRecords(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	customer := harness.customer(ctx, t)
	versionID := harness.catalog(ctx, t, "premium", 49000)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := harness.pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("%v\n%s", err, query)
		}
	}
	exec(`UPDATE plan_localizations SET name = 'Премиум' WHERE plan_id = (SELECT plan_id FROM plan_versions WHERE id = $1::uuid) AND locale = 'ru'`, versionID)
	exec(`INSERT INTO identities (user_id, provider, provider_subject, verified_at, metadata)
		VALUES ($1::uuid, 'telegram', '777000', now(), '{"username":"ivan"}')`, customer)
	var orderID string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO orders (user_id, state, operation, currency, subtotal_minor,
			external_minor, paid_minor, paid_at, idempotency_key, selected_squad_ids, period_seconds)
		VALUES ($1::uuid, 'fulfilled', 'extension', 'RUB', 149000, 149000, 149000, now(), 'notice-order', '{}', 7776000)
		RETURNING id::text`, customer).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO order_lines (order_id, plan_id, plan_version_id, unit_amount_minor, snapshot)
		SELECT $1::uuid, plan_id, id, 149000, '{}' FROM plan_versions WHERE id = $2::uuid`, orderID, versionID)
	exec(`INSERT INTO payment_intents (order_id, provider, status, amount_minor, currency, idempotency_key, method)
		VALUES ($1::uuid, 'yookassa', 'succeeded', 149000, 'RUB', 'notice-intent', 'sbp')`, orderID)
	exec(`INSERT INTO legacy_payments (user_id, source, source_id, amount_minor, currency, paid_at)
		VALUES ($1::uuid, 'bedolaga', 1, 19900, 'RUB', now() - interval '90 days')`, customer)
	harness.seedWallet(ctx, t, customer, "notice-wallet", 75000)

	config := operator.Config{Locale: "ru", Location: time.UTC, PanelURL: "https://lk.example.test"}
	text, markup := operator.RenderNotice(ctx, harness.pool, "renewal", map[string]any{
		"orderId": orderID, "operation": "extension", "currency": "RUB", "amountMinor": float64(149000),
	}, time.Now(), config)
	for _, want := range []string{"♻️ Продление", "@ivan", "777000", "<b>Премиум</b> · 3 месяца", "1 490 ₽", "YooKassa · СБП", "2-я оплата"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the renewal lacks %q:\n%s", want, text)
		}
	}
	if markup == nil || len(markup.InlineKeyboard) == 0 {
		t.Fatal("the renewal has no panel buttons")
	}

	topup, _ := operator.RenderNotice(ctx, harness.pool, "topup", map[string]any{
		"orderId": orderID, "currency": "RUB", "creditedMinor": float64(50000), "classification": "paid",
	}, time.Now(), config)
	if !strings.Contains(topup, "Баланс теперь: <b>750 ₽</b>") {
		t.Fatalf("the top-up does not show the wallet:\n%s", topup)
	}

	var ticketID string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO support_tickets (user_id, queue_id, subject, priority)
		SELECT $1::uuid, id, 'my card was charged twice', 'high' FROM support_queues WHERE code = 'general'
		RETURNING id::text`, customer).Scan(&ticketID); err != nil {
		t.Fatal(err)
	}
	ticket, _ := operator.RenderNotice(ctx, harness.pool, "support", map[string]any{"ticketId": ticketID}, time.Now(), config)
	for _, want := range []string{"💬 Новое обращение", "Общие вопросы", "высокий", "@ivan"} {
		if !strings.Contains(ticket, want) {
			t.Fatalf("the ticket lacks %q:\n%s", want, ticket)
		}
	}
	if strings.Contains(ticket, "charged twice") {
		t.Fatalf("the ticket's subject reached the group:\n%s", ticket)
	}

	// A notice naming records that do not exist still renders.
	missing, _ := operator.RenderNotice(ctx, harness.pool, "topup", map[string]any{
		"orderId": "00000000-0000-4000-8000-000000000001", "currency": "RUB", "creditedMinor": float64(100),
	}, time.Now(), config)
	if !strings.Contains(missing, "+1 ₽") {
		t.Fatalf("a notice without records:\n%s", missing)
	}
}
