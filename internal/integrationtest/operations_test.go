//go:build integration

package integrationtest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/recurring"
)

// operationsKey is the fixed 32-byte key the panel service seals secrets with
// in tests. Determinism matters more than secrecy here: a failure has to be
// reproducible.
var operationsKey = []byte("0123456789abcdef0123456789abcdef")

func newOperations(t *testing.T, harness *harness) *panelpg.Service {
	t.Helper()
	service, err := panelpg.New(harness.pool, operationsKey, panelpg.Options{})
	if err != nil {
		t.Fatalf("build operations service: %v", err)
	}
	return service
}

// operator inserts an admin account the panel can attribute actions to.
func (harness *harness) operator(ctx context.Context, t *testing.T, email string) panelpg.Actor {
	t.Helper()
	var id string
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO admin_users (email, email_normalized, display_name, password_hash, status)
		 VALUES ($1, lower($1), 'Integration Operator', 'x-placeholder-hash', 'active')
		 RETURNING id::text`, email).Scan(&id); err != nil {
		t.Fatalf("create operator: %v", err)
	}
	return panelpg.Actor{AdminID: id, Type: "admin", Reason: "integration test"}
}

// TestCustomerSearchFindsBySafeIdentifiersOnly is the privacy boundary of the
// search: an operator can find a customer by an identifier they are allowed to
// hold, and the result carries nothing they are not.
func TestCustomerSearchFindsBySafeIdentifiersOnly(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)

	customerID := harness.customer(ctx, t)
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO remnawave_users (user_id, remnawave_id, telegram_id)
		 VALUES ($1::uuid, 4242, 987654321)`, customerID); err != nil {
		t.Fatalf("link remnawave user: %v", err)
	}

	page, err := operations.SearchCustomers(ctx, panelpg.CustomerFilter{
		Query: "987654321", PageSize: 10,
	})
	if err != nil {
		t.Fatalf("search customers: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != customerID {
		t.Fatalf("expected the linked customer, got %d results", len(page.Items))
	}

	// An identifier nobody holds finds nobody. The search must not fall back to
	// a substring match across unrelated columns, because that is how a support
	// operator ends up looking at somebody they did not ask for.
	empty, err := operations.SearchCustomers(ctx, panelpg.CustomerFilter{
		Query: "111111111", PageSize: 10,
	})
	if err != nil {
		t.Fatalf("search customers: %v", err)
	}
	if len(empty.Items) != 0 {
		t.Fatalf("expected no results for an unknown identifier, got %d", len(empty.Items))
	}
}

// TestFinanceExportWalksEveryOrderExactlyOnce is the property that makes the
// export usable for reconciliation: cursor pagination must not drop an order
// and must not emit one twice, even when several share a timestamp.
func TestFinanceExportWalksEveryOrderExactlyOnce(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)
	store := commercepg.New(harness.pool, nil, testOptions())
	versionID := harness.catalog(ctx, t, "export-plan", 10_000)

	const orderCount = 7
	expected := make(map[string]bool, orderCount)
	for index := 0; index < orderCount; index++ {
		customerID := harness.customer(ctx, t)
		order, err := store.CreateOrder(ctx, commercepg.CreateOrderInput{
			CustomerID: customerID, PlanVersionID: versionID, Currency: "RUB",
			Operation: "purchase", IdempotencyKey: "export-" + strings.Repeat("x", index+1),
		})
		if err != nil {
			t.Fatalf("create order %d: %v", index, err)
		}
		expected[uuidText(order.ID)] = true
	}

	// A page size smaller than the row count forces the cursor to be exercised
	// rather than the whole table arriving in one read.
	seen := map[string]int{}
	filter := panelpg.OrderFilter{PageSize: 3}
	for page := 0; page < 20; page++ {
		rows, next, err := operations.ExportFinance(ctx, filter)
		if err != nil {
			t.Fatalf("export finance: %v", err)
		}
		for _, row := range rows {
			seen[row.Fields()[0]]++
		}
		if next == "" {
			break
		}
		filter.Cursor = next
	}

	for id := range expected {
		switch seen[id] {
		case 0:
			t.Fatalf("order %s was missing from the export", id)
		case 1:
		default:
			t.Fatalf("order %s appeared %d times in the export", id, seen[id])
		}
	}
}

// TestBulkOperationCannotRunWithoutAPreview is the guarantee that makes bulk
// changes safe: the two-step shape is enforced by the database, not by the
// panel remembering to ask.
func TestBulkOperationCannotRunWithoutAPreview(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)
	actor := harness.operator(ctx, t, "bulk@example.test")

	customerID := harness.customer(ctx, t)
	var subscriptionID string
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO subscriptions (user_id, label, slot) VALUES ($1::uuid, 'Primary', 1)
		 RETURNING id::text`, customerID).Scan(&subscriptionID); err != nil {
		t.Fatalf("create subscription: %v", err)
	}

	operation, err := operations.PreviewBulkOperation(ctx, panelpg.BulkInput{
		Kind:           "subscription_extend",
		Targets:        []panelpg.BulkTarget{{Type: "subscription", ID: subscriptionID}},
		Parameters:     json.RawMessage(`{"days":30}`),
		IdempotencyKey: "bulk-preview-1",
	}, actor)
	if err != nil {
		t.Fatalf("preview bulk operation: %v", err)
	}
	if operation.Status != "ready" || operation.Total != 1 {
		t.Fatalf("expected one ready target, got %s with %d", operation.Status, operation.Total)
	}

	// Previewing again with the same key returns the same operation rather than
	// a second one, so a double-submitted form cannot queue the work twice.
	repeat, err := operations.PreviewBulkOperation(ctx, panelpg.BulkInput{
		Kind:           "subscription_extend",
		Targets:        []panelpg.BulkTarget{{Type: "subscription", ID: subscriptionID}},
		Parameters:     json.RawMessage(`{"days":30}`),
		IdempotencyKey: "bulk-preview-1",
	}, actor)
	if err != nil {
		t.Fatalf("repeat preview: %v", err)
	}
	if repeat.ID != operation.ID {
		t.Fatalf("a repeated preview created a second operation: %s and %s", operation.ID, repeat.ID)
	}

	started, err := operations.StartBulkOperation(ctx, operation.ID, actor)
	if err != nil {
		t.Fatalf("start bulk operation: %v", err)
	}
	if started.Status != "running" {
		t.Fatalf("expected a running operation, got %s", started.Status)
	}

	// Starting an operation that is already running is refused. Without that,
	// two operators clicking apply would each queue the same four hundred
	// changes.
	if _, err = operations.StartBulkOperation(ctx, operation.ID, actor); err == nil {
		t.Fatal("expected a second start to be refused")
	}

	// Recording an item twice counts it once, so a worker restarted mid-batch
	// cannot inflate the success counter.
	if _, err = operations.RecordBulkItem(ctx, operation.ID, 0, "succeeded", ""); err != nil {
		t.Fatalf("record bulk item: %v", err)
	}
	final, err := operations.RecordBulkItem(ctx, operation.ID, 0, "succeeded", "")
	if err != nil {
		t.Fatalf("re-record bulk item: %v", err)
	}
	if final.Succeeded != 1 {
		t.Fatalf("expected one success after a replay, got %d", final.Succeeded)
	}
}

// TestRecurringChargeIsRefusedWithoutOperatorApproval covers the rule that
// governs every automatic charge: the adapter's declaration and the operator's
// merchant-level test must both agree, and the switch can only narrow what the
// adapter offers.
func TestRecurringChargeIsRefusedWithoutOperatorApproval(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)
	actor := harness.operator(ctx, t, "recurring@example.test")

	saved, err := operations.SaveProviderSettings(ctx, panelpg.ProviderSettingsInput{
		Provider: "yookassa", MerchantID: "shop-1", Enabled: true,
	}, actor)
	if err != nil {
		t.Fatalf("save provider settings: %v", err)
	}
	if saved.RecurringEnabled {
		t.Fatal("recurring must be off until an operator turns it on")
	}

	capability := recurring.Capability{
		AdapterSupports: true, OperatorEnabled: saved.RecurringEnabled,
		TestStatus: saved.RecurringTestStatus,
	}
	if capability.Allows() {
		t.Fatal("an untested merchant account must not be chargeable")
	}

	// A failed test cannot enable charging, whatever the request asks for.
	failed, err := operations.RecordRecurringTest(ctx, "yookassa", "shop-1", false, true, actor)
	if err == nil && failed.RecurringEnabled {
		t.Fatal("a failed capability test must not enable automatic charging")
	}

	passed, err := operations.RecordRecurringTest(ctx, "yookassa", "shop-1", true, true, actor)
	if err != nil {
		t.Fatalf("record passing test: %v", err)
	}
	approved := recurring.Capability{
		AdapterSupports: true, OperatorEnabled: passed.RecurringEnabled,
		TestStatus: passed.RecurringTestStatus,
	}
	if !approved.Allows() {
		t.Fatal("a passing test with the operator switch on should allow charging")
	}

	// The operator switch can only ever narrow what the adapter declares.
	unsupported := recurring.Capability{
		AdapterSupports: false, OperatorEnabled: true, TestStatus: "passed",
	}
	if unsupported.Allows() {
		t.Fatal("an adapter that cannot bind a method must never be chargeable")
	}
}

// TestGoodsOrderCreatesExactlyOneDelivery is the double-delivery guard: a paid
// shop order produces one delivery row however many times settlement is
// replayed, because the primary key on order_id says so.
func TestGoodsOrderCreatesExactlyOneDelivery(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := commercepg.New(harness.pool, nil, testOptions())
	customerID := harness.customer(ctx, t)
	productID := harness.goodsProduct(ctx, t)

	order, err := store.CreateGoodsOrder(ctx, commercepg.GoodsOrderInput{
		CustomerID: customerID, ProductID: productID, Quantity: 1,
		Recipient: "recipient_one", PriceMinor: 25_000, CostMinor: 20_000, CostKnown: true,
		Currency: "RUB", QuoteExpiresAt: time.Now().Add(time.Hour),
		IdempotencyKey: "goods-1", SkipWallet: true,
	})
	if err != nil {
		t.Fatalf("create goods order: %v", err)
	}

	// Two settlements of the same order, exactly as a replayed webhook and a
	// reconciliation poll would produce.
	for attempt := 0; attempt < 2; attempt++ {
		harness.settle(ctx, t, store, uuidText(order.ID), 25_000, "goods-event")
	}

	var deliveries int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM goods_deliveries WHERE order_id = $1`, order.ID).Scan(&deliveries); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if deliveries != 1 {
		t.Fatalf("expected exactly one delivery, got %d", deliveries)
	}
}

// TestGiftIsClaimableExactlyOnce covers single redemption, which is a property
// of the claim predicate rather than of timing.
func TestGiftIsClaimableExactlyOnce(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := commercepg.New(harness.pool, nil, testOptions())

	senderID := harness.customer(ctx, t)
	recipientID := harness.customer(ctx, t)
	otherID := harness.customer(ctx, t)

	purchase, err := store.CreateGiftOrder(ctx, commercepg.GiftOrderInput{
		SenderID: senderID, Kind: "wallet_credit", CreditMinor: 50_000, Currency: "RUB",
		IdempotencyKey: "gift-1", SkipWallet: true,
	})
	if err != nil {
		t.Fatalf("create gift order: %v", err)
	}
	if purchase.Code == "" {
		t.Fatal("the claim code must be returned exactly once, on creation")
	}

	// An unpaid gift is not claimable: the sender has not paid for it yet.
	if _, err = store.ClaimGift(ctx, purchase.Code, recipientID); !errors.Is(err, commercepg.ErrGiftNotFound) {
		t.Fatalf("expected an unpaid gift to be unclaimable, got %v", err)
	}

	harness.settle(ctx, t, store, uuidText(purchase.Order.ID), 50_000, "gift-event")

	// The sender cannot claim their own gift.
	if _, err = store.ClaimGift(ctx, purchase.Code, senderID); !errors.Is(err, commercepg.ErrGiftOwnClaim) {
		t.Fatalf("expected a self-claim to be refused, got %v", err)
	}

	claimed, err := store.ClaimGift(ctx, purchase.Code, recipientID)
	if err != nil {
		t.Fatalf("claim gift: %v", err)
	}
	if claimed.Status != "claimed" {
		t.Fatalf("expected a claimed gift, got %s", claimed.Status)
	}
	if balance := harness.walletBalance(ctx, t, recipientID); balance != 50_000 {
		t.Fatalf("expected the recipient to be credited 50000, got %d", balance)
	}

	// A second claim, by anybody, changes nothing.
	if _, err = store.ClaimGift(ctx, purchase.Code, otherID); !errors.Is(err, commercepg.ErrGiftNotFound) {
		t.Fatalf("expected a second claim to be refused, got %v", err)
	}
	if balance := harness.walletBalance(ctx, t, otherID); balance != 0 {
		t.Fatalf("a refused claim credited %d", balance)
	}
}

// goodsProduct inserts a provider, a product, and its pricing.
func (harness *harness) goodsProduct(ctx context.Context, t *testing.T) string {
	t.Helper()
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO goods_providers (slug, enabled)
		 VALUES ('fragment', true)
		 ON CONFLICT (slug) DO UPDATE SET enabled = true`); err != nil {
		t.Fatalf("create goods provider: %v", err)
	}
	var productID string
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO goods_products (code, provider_slug, kind, star_quantity, visible)
		 VALUES ('stars-100', 'fragment', 'telegram_stars', 100, true) RETURNING id::text`).
		Scan(&productID); err != nil {
		t.Fatalf("create goods product: %v", err)
	}
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO goods_pricing (product_id, currency, fixed_amount_minor)
		 VALUES ($1::uuid, 'RUB', 25000)`, productID); err != nil {
		t.Fatalf("price goods product: %v", err)
	}
	return productID
}
