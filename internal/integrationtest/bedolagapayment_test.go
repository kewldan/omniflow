//go:build integration

package integrationtest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/payments"
	"github.com/omniflow/omniflow/internal/paymentservice"
)

// fakeLegacyYooKassa stands in for YooKassa: an unsigned notification is
// parsed as is, and the authenticated poll answers from what the test set.
type fakeLegacyYooKassa struct {
	mu       sync.Mutex
	payments map[string]payments.Intent
	polls    int
}

func (fake *fakeLegacyYooKassa) Name() string { return "yookassa" }
func (fake *fakeLegacyYooKassa) Capabilities() payments.Capabilities {
	return payments.Capabilities{Webhooks: true, Polling: true}
}
func (fake *fakeLegacyYooKassa) WebhooksNeedPolling() bool { return true }
func (fake *fakeLegacyYooKassa) Create(context.Context, payments.CreateRequest) (payments.Intent, error) {
	return payments.Intent{}, payments.ErrUnsupported
}
func (fake *fakeLegacyYooKassa) Refund(context.Context, payments.RefundRequest) (payments.Refund, error) {
	return payments.Refund{}, payments.ErrUnsupported
}
func (fake *fakeLegacyYooKassa) Poll(_ context.Context, reference string) (payments.Intent, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.polls++
	intent, ok := fake.payments[reference]
	if !ok {
		return payments.Intent{}, errors.New("not found")
	}
	return intent, nil
}
func (fake *fakeLegacyYooKassa) VerifyWebhook(_ http.Header, body []byte) (payments.WebhookEvent, error) {
	var notification struct {
		ID       string            `json:"id"`
		Status   string            `json:"status"`
		Metadata map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(body, &notification); err != nil {
		return payments.WebhookEvent{}, err
	}
	return payments.WebhookEvent{
		// Each body is its own notification, as YooKassa's digest-suffixed
		// event IDs are.
		ID: "payment.succeeded:" + notification.ID + ":" + strconv.Itoa(len(body)), ProviderReference: notification.ID,
		Status: notification.Status, Metadata: notification.Metadata,
	}, nil
}

// A top-up started in Bedolaga and paid after the cutover is credited to the
// customer the import carried over, once; one that cannot be attributed is
// queued for an operator with a notice; an Omniflow-shaped unknown payment is
// refused as before, without a provider request.
func TestBedolagaPaymentAfterCutover(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	fake := &fakeLegacyYooKassa{payments: map[string]payments.Intent{}}
	service := paymentservice.New(harness.pool, store, fake)

	customer := harness.customer(ctx, t)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := harness.pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("%v\n%s", err, query)
		}
	}
	exec(`INSERT INTO identities (user_id, provider, provider_subject, verified_at) VALUES ($1::uuid, 'telegram', '777000', now())`, customer)
	var runID string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO bedolaga_import_runs (actor, status, finished_at)
		VALUES ('test', 'completed', now()) RETURNING id::text`).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO bedolaga_import_items (kind, source_id, status, user_id, fingerprint, last_run_id, applied_at)
		VALUES ('user', 501, 'applied', $1::uuid, decode(repeat('00', 32), 'hex'), $2::uuid, now())`, customer, runID)

	rub := func(amount int64) commerce.Money { return commerce.Money{Amount: amount, Currency: "RUB"} }
	fake.payments["yk-topup"] = payments.Intent{ProviderReference: "yk-topup", Status: "succeeded", Amount: rub(50000),
		Metadata: map[string]string{"user_id": "501", "user_telegram_id": "777000", "type": "balance_topup"}}
	fake.payments["yk-stranger"] = payments.Intent{ProviderReference: "yk-stranger", Status: "succeeded", Amount: rub(30000),
		Metadata: map[string]string{"user_id": "999", "type": "balance_topup"}}
	fake.payments["yk-guest"] = payments.Intent{ProviderReference: "yk-guest", Status: "succeeded", Amount: rub(19900),
		Metadata: map[string]string{"user_id": "", "purchase_token": "tok"}}
	fake.payments["yk-wrong-tg"] = payments.Intent{ProviderReference: "yk-wrong-tg", Status: "succeeded", Amount: rub(10000),
		Metadata: map[string]string{"user_id": "501", "user_telegram_id": "123", "type": "balance_topup"}}
	fake.payments["yk-pending"] = payments.Intent{ProviderReference: "yk-pending", Status: "pending", Amount: rub(10000),
		Metadata: map[string]string{"user_id": "501"}}

	deliver := func(id string, metadata map[string]string) (string, error) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"id": id, "status": "succeeded", "metadata": metadata})
		return service.HandleWebhook(ctx, "yookassa", http.Header{}, body)
	}
	if outcome, err := deliver("yk-topup", map[string]string{"user_id": "501"}); err != nil || outcome != paymentservice.RescueCredited {
		t.Fatalf("a carried customer's top-up: %s %v", outcome, err)
	}
	if balance := harness.walletBalance(ctx, t, customer); balance != 50000 {
		t.Fatalf("wallet = %d, want 50000", balance)
	}
	var told int
	_ = harness.pool.QueryRow(ctx, `SELECT count(*) FROM customer_event_notices WHERE user_id = $1::uuid
		AND kind = 'wallet_credited' AND (params->>'amountMinor')::bigint = 50000`, customer).Scan(&told)
	if told != 1 {
		t.Fatal("the customer is told the payment arrived")
	}
	// The same payment again, under a fresh notification: credited once.
	body, _ := json.Marshal(map[string]any{"id": "yk-topup", "status": "succeeded", "metadata": map[string]string{"user_id": "501"}, "again": true})
	if outcome, err := service.HandleWebhook(ctx, "yookassa", http.Header{}, body); err != nil || outcome != paymentservice.RescueDuplicate {
		t.Fatalf("a second notification: %s %v", outcome, err)
	}
	if balance := harness.walletBalance(ctx, t, customer); balance != 50000 {
		t.Fatalf("credited twice: wallet = %d", balance)
	}

	for id, reason := range map[string]string{
		"yk-stranger": paymentservice.ReviewNoCustomer,
		"yk-guest":    paymentservice.ReviewGuestPurchase,
		"yk-wrong-tg": paymentservice.ReviewTelegramMismatch,
	} {
		if outcome, err := deliver(id, map[string]string{"user_id": "1", "purchase_token": "x"}); err != nil || outcome != paymentservice.RescueNeedsReview {
			t.Fatalf("%s: %s %v", id, outcome, err)
		}
		var status, stored string
		if err := harness.pool.QueryRow(ctx, `SELECT status, reason FROM bedolaga_payment_claims WHERE provider_reference = $1`, id).
			Scan(&status, &stored); err != nil || status != "needs_review" || stored != reason {
			t.Fatalf("%s queued as %s/%s (%v), want needs_review/%s", id, status, stored, err, reason)
		}
	}
	if balance := harness.walletBalance(ctx, t, customer); balance != 50000 {
		t.Fatalf("a queued payment moved money: wallet = %d", balance)
	}
	var notices int
	_ = harness.pool.QueryRow(ctx, `SELECT count(*) FROM operator_notifications WHERE kind = 'topup' AND dedupe_key LIKE 'bedolaga-payment:%'`).Scan(&notices)
	if notices != 4 {
		t.Fatalf("operator notices = %d, want one per payment", notices)
	}

	// The operator deals with one and resolves it with a note; a credited
	// payment cannot be resolved.
	operations := newOperations(t, harness)
	actor := harness.operator(ctx, t, "rescue@omniflow.test")
	queue, err := operations.BedolagaPaymentClaims(ctx, "needs_review", 50)
	if err != nil || len(queue) != 3 {
		t.Fatalf("queue: %d %v", len(queue), err)
	}
	resolved, err := operations.ResolveBedolagaPaymentClaim(ctx, queue[0].ID, "Refunded in the YooKassa dashboard", actor)
	if err != nil || resolved.Status != "resolved" || resolved.ResolutionNote == "" {
		t.Fatalf("resolve: %+v %v", resolved, err)
	}
	credited, err := operations.BedolagaPaymentClaims(ctx, "credited", 50)
	if err != nil || len(credited) != 1 {
		t.Fatalf("credited: %d %v", len(credited), err)
	}
	if _, err = operations.ResolveBedolagaPaymentClaim(ctx, credited[0].ID, "no", actor); err == nil {
		t.Fatal("a credited payment cannot be resolved")
	}

	if outcome, err := deliver("yk-pending", map[string]string{"user_id": "501"}); err != nil || outcome != paymentservice.RescueNotPaid {
		t.Fatalf("an unpaid payment: %s %v", outcome, err)
	}

	// An unknown payment with Omniflow's metadata, or none, is refused as
	// before and costs no provider request.
	before := fake.polls
	if _, err := deliver("yk-ours", map[string]string{"order_id": "6f0c6b0e-0000-4000-8000-000000000001"}); !errors.Is(err, paymentservice.ErrWebhookRejected) {
		t.Fatalf("an unknown Omniflow payment is refused: %v", err)
	}
	if fake.polls != before {
		t.Fatal("a refused notification must not reach the provider")
	}

	// Long after the migration nothing is rescued.
	exec(`UPDATE bedolaga_import_runs SET finished_at = now() - interval '40 days', started_at = now() - interval '41 days'`)
	fake.payments["yk-late"] = payments.Intent{ProviderReference: "yk-late", Status: "succeeded", Amount: rub(10000),
		Metadata: map[string]string{"user_id": "501"}}
	if _, err := deliver("yk-late", map[string]string{"user_id": "501"}); !errors.Is(err, paymentservice.ErrWebhookRejected) {
		t.Fatalf("outside the window the payment is refused: %v", err)
	}
}
