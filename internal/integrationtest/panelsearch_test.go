//go:build integration

package integrationtest

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/accesscode"
	"github.com/omniflow/omniflow/internal/panelsearch"
	"github.com/omniflow/omniflow/internal/panelsearchpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// searchFixture is one installation with one of everything the palette finds.
type searchFixture struct {
	customer    string
	other       string
	order       string
	reference   string
	ticket      string
	giftCode    string
	accessCode  string
	accessBatch string
}

func seedSearchFixture(ctx context.Context, t *testing.T, harness *harness) searchFixture {
	t.Helper()
	fixture := searchFixture{reference: "yk-2f9c1a3e-000f-5000-9000-1b2c3d4e5f6a"}
	fixture.customer = seedCustomer(ctx, t, harness, "")
	fixture.other = seedCustomer(ctx, t, harness, "")

	exec := func(statement string, arguments ...any) {
		t.Helper()
		if _, err := harness.pool.Exec(ctx, statement, arguments...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}

	exec(`INSERT INTO remnawave_users (user_id, remnawave_id, telegram_id) VALUES ($1, 501, 777000111)`, fixture.customer)
	// A web customer known only by a Telegram sign-in identity.
	exec(`INSERT INTO identities (user_id, provider, provider_subject) VALUES ($1, 'telegram', '888000222')`, fixture.other)

	mac := hmac.New(sha256.New, adminTestKey)
	_, _ = mac.Write([]byte("ivan@example.com"))
	exec(`INSERT INTO contact_channels (user_id, kind, value_fingerprint) VALUES ($1, 'email', $2)`,
		fixture.customer, mac.Sum(nil))
	exec(`INSERT INTO subscriptions (user_id, slot, label, remnawave_username) VALUES ($1, 1, 'Main', 'omniflow_abc123def4567890')`,
		fixture.customer)

	fixture.order = seedPendingOrder(ctx, t, harness, fixture.customer, "USD", 499)
	exec(`INSERT INTO payment_intents (order_id, provider, amount_minor, currency, idempotency_key, provider_reference)
	      VALUES ($1, 'yookassa', 499, 'USD', 'search-intent', $2)`, fixture.order, fixture.reference)

	if err := harness.pool.QueryRow(ctx, `
		INSERT INTO support_tickets (user_id, subject, queue_id)
		VALUES ($1, 'Refund for the March renewal', (SELECT id FROM support_queues WHERE code = 'general'))
		RETURNING id`, fixture.customer,
	).Scan(&fixture.ticket); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	var giftHint string
	var err error
	fixture.giftCode, giftHint, err = accesscode.New()
	if err != nil {
		t.Fatalf("gift code: %v", err)
	}
	giftOrder := seedPendingOrder(ctx, t, harness, fixture.other, "EUR", 100)
	exec(`INSERT INTO gifts (order_id, sender_user_id, kind, credit_minor, currency, code_hash, code_hint, expires_at)
	      VALUES ($1, $2, 'wallet_credit', 100, 'EUR', $3, $4, now() + interval '30 days')`,
		giftOrder, fixture.other, accesscode.Hash(fixture.giftCode), giftHint)

	var accessHint string
	fixture.accessCode, accessHint, err = accesscode.New()
	if err != nil {
		t.Fatalf("access code: %v", err)
	}
	planVersion := seedPlanVersion(ctx, t, harness)
	if err = harness.pool.QueryRow(ctx, `
		INSERT INTO code_batches (reference, plan_version_id, quantity, unit_price_minor, currency)
		VALUES ('Distributor March', $1, 1, 0, 'USD') RETURNING id`, planVersion,
	).Scan(&fixture.accessBatch); err != nil {
		t.Fatalf("seed batch: %v", err)
	}
	exec(`INSERT INTO access_codes (batch_id, code_hash, code_hint) VALUES ($1, $2, $3)`,
		fixture.accessBatch, accesscode.Hash(fixture.accessCode), accessHint)
	return fixture
}

func runSearch(
	t *testing.T, ctx context.Context, service *panelsearchpg.Service, input string, role rbac.Role,
) panelsearchpg.Results {
	t.Helper()
	query, ok := panelsearch.Parse(input)
	if !ok {
		t.Fatalf("%q is not searchable", input)
	}
	results, err := service.Search(ctx, query, panelsearch.Visible(rbac.NewGrant(role)))
	if err != nil {
		t.Fatalf("search %q: %v", input, err)
	}
	return results
}

// Every identifier an operator is handed finds the record it names.
func TestPanelSearchFindsRecordsByEveryIdentifier(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	fixture := seedSearchFixture(ctx, t, harness)
	service, err := panelsearchpg.New(harness.pool, adminTestKey)
	if err != nil {
		t.Fatalf("search service: %v", err)
	}

	customerHit := func(input, want, matchedBy string) {
		t.Helper()
		results := runSearch(t, ctx, service, input, rbac.RoleOwner)
		for _, hit := range results.Customers {
			if hit.ID == want && hit.MatchedBy == matchedBy {
				return
			}
		}
		t.Fatalf("%q did not find customer %s by %s: %+v", input, want, matchedBy, results.Customers)
	}
	customerHit("777000111", fixture.customer, "telegramId")
	customerHit("888000222", fixture.other, "telegramId")
	customerHit("  IVAN@example.com ", fixture.customer, "email")
	customerHit("omniflow_abc123def4567890", fixture.customer, "remnawaveUsername")
	customerHit(fixture.customer[:8], fixture.customer, "id")
	customerHit(strings.ToUpper(fixture.customer), fixture.customer, "id")

	// An email is matched exactly: a fragment of one finds nothing.
	if results := runSearch(t, ctx, service, "ivan@example.co", rbac.RoleOwner); len(results.Customers) != 0 {
		t.Fatalf("a partial address matched: %+v", results.Customers)
	}

	results := runSearch(t, ctx, service, fixture.reference, rbac.RoleOwner)
	if len(results.Orders) != 1 || results.Orders[0].ID != fixture.order || results.Orders[0].Provider != "yookassa" {
		t.Fatalf("provider reference search = %+v", results.Orders)
	}
	results = runSearch(t, ctx, service, fixture.order[:13], rbac.RoleOwner)
	if len(results.Orders) == 0 || results.Orders[0].ID != fixture.order {
		t.Fatalf("order prefix search = %+v", results.Orders)
	}

	results = runSearch(t, ctx, service, "march RENEWAL", rbac.RoleOwner)
	if len(results.Tickets) != 1 || results.Tickets[0].ID != fixture.ticket || results.Tickets[0].MatchedBy != "subject" {
		t.Fatalf("subject search = %+v", results.Tickets)
	}
	results = runSearch(t, ctx, service, fixture.ticket[:8], rbac.RoleOwner)
	if len(results.Tickets) == 0 || results.Tickets[0].ID != fixture.ticket {
		t.Fatalf("ticket prefix search = %+v", results.Tickets)
	}
	// A wildcard is searched for literally rather than matching everything.
	if results = runSearch(t, ctx, service, "%%%", rbac.RoleOwner); len(results.Tickets) != 0 {
		t.Fatalf("a wildcard matched tickets: %+v", results.Tickets)
	}

	grouped := strings.ToLower(fixture.giftCode[:4] + "-" + fixture.giftCode[4:8] + " " + fixture.giftCode[8:])
	results = runSearch(t, ctx, service, grouped, rbac.RoleOwner)
	if len(results.Gifts) != 1 || len(results.AccessCodes) != 0 {
		t.Fatalf("gift code search = %+v / %+v", results.Gifts, results.AccessCodes)
	}
	results = runSearch(t, ctx, service, fixture.accessCode, rbac.RoleOwner)
	if len(results.AccessCodes) != 1 || results.AccessCodes[0].BatchReference != "Distributor March" {
		t.Fatalf("access code search = %+v", results.AccessCodes)
	}

	results = runSearch(t, ctx, service, "@ivan_petrov", rbac.RoleOwner)
	if !results.HandleNotStored || len(results.Customers) != 0 {
		t.Fatalf("a handle search = %+v", results)
	}
}

// A group the role cannot read is never queried, so it comes back nil — not
// empty — and transport leaves it out of the response.
func TestPanelSearchNeverReturnsAGroupTheRoleCannotRead(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	fixture := seedSearchFixture(ctx, t, harness)
	service, err := panelsearchpg.New(harness.pool, adminTestKey)
	if err != nil {
		t.Fatalf("search service: %v", err)
	}

	// Support works tickets and customers; orders are finance's.
	results := runSearch(t, ctx, service, fixture.reference, rbac.RoleSupport)
	if results.Orders != nil {
		t.Fatalf("support saw orders: %+v", results.Orders)
	}
	results = runSearch(t, ctx, service, "march renewal", rbac.RoleSupport)
	if len(results.Tickets) != 1 {
		t.Fatalf("support could not find the ticket: %+v", results.Tickets)
	}

	// Finance sees the order and not the support desk.
	results = runSearch(t, ctx, service, fixture.reference, rbac.RoleFinance)
	if len(results.Orders) != 1 {
		t.Fatalf("finance could not find the order: %+v", results.Orders)
	}
	if results = runSearch(t, ctx, service, "march renewal", rbac.RoleFinance); results.Tickets != nil {
		t.Fatalf("finance saw tickets: %+v", results.Tickets)
	}

	// Marketing reads customers and the catalogue, and nothing about money,
	// tickets, or gifts.
	results = runSearch(t, ctx, service, fixture.giftCode, rbac.RoleMarketing)
	if results.Gifts != nil || results.Orders != nil || results.Tickets != nil {
		t.Fatalf("marketing saw %+v", results)
	}

	// No grant, no queries at all.
	query, _ := panelsearch.Parse("777000111")
	results, err = service.Search(ctx, query, panelsearch.Visible(rbac.NewGrant()))
	if err != nil || results.Customers != nil {
		t.Fatalf("an empty grant searched customers: %+v %v", results.Customers, err)
	}
}

// Each lookup is served by an index rather than a scan. The tables here are
// tiny, so the planner would choose a sequential scan on cost alone; turning
// that off asks the narrower question that matters — whether an index able to
// answer the predicate exists at all.
func TestPanelSearchLookupsAreIndexed(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	seedSearchFixture(ctx, t, harness)

	connection, err := harness.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer connection.Release()
	if _, err = connection.Exec(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatalf("disable seqscan: %v", err)
	}
	const prefixRange = `BETWEEN '3f000000-0000-0000-0000-000000000000' AND '3fffffff-ffff-ffff-ffff-ffffffffffff'`
	cases := map[string]string{
		`SELECT id FROM support_tickets WHERE subject ILIKE '%renewal%'`:                           "support_tickets_subject_trgm_idx",
		`SELECT id FROM payment_intents WHERE provider_reference = 'x'`:                            "payment_intents_provider_reference_lookup_idx",
		`SELECT id FROM users WHERE id ` + prefixRange:                                             "users_pkey",
		`SELECT id FROM orders WHERE id ` + prefixRange:                                            "orders_pkey",
		`SELECT id FROM support_tickets WHERE id ` + prefixRange:                                   "support_tickets_pkey",
		`SELECT user_id FROM subscriptions WHERE remnawave_username = ANY(ARRAY['a'])`:             "subscriptions_username_idx",
		`SELECT user_id FROM remnawave_users WHERE telegram_id = 1`:                                "remnawave_users_telegram_id_key",
		`SELECT id FROM gifts WHERE code_hash = '\x00'`:                                            "gifts_code_hash_key",
		`SELECT id FROM access_codes WHERE code_hash = '\x00'`:                                     "access_codes_code_hash_key",
		`SELECT user_id FROM contact_channels WHERE kind = 'email' AND value_fingerprint = '\x00'`: "contact_channels_kind_value_fingerprint_key",
	}
	for statement, index := range cases {
		var plan strings.Builder
		rows, queryErr := connection.Query(ctx, "EXPLAIN "+statement)
		if queryErr != nil {
			t.Fatalf("explain %s: %v", statement, queryErr)
		}
		for rows.Next() {
			var line string
			if scanErr := rows.Scan(&line); scanErr != nil {
				t.Fatalf("scan plan: %v", scanErr)
			}
			plan.WriteString(line + "\n")
		}
		rows.Close()
		if !strings.Contains(plan.String(), index) {
			t.Fatalf("%s is not served by %s:\n%s", statement, index, plan.String())
		}
	}
}
