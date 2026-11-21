//go:build integration

package integrationtest

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/adminauthpg"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	apihttp "github.com/omniflow/omniflow/internal/httpapi"
)

// /v1/admin is the machine surface: it bypasses RBAC and per-operator audit by
// design. Its state-changing requests must still reach the audit trail, under
// an actor that says the shared token did it.
func TestOperatorTokenMutationsAreAudited(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	const token = "operator-token-for-integration"
	handlers := apihttp.NewCommerceHandlers(dbgen.New(harness.pool), nil, nil, nil, nil, nil, nil, nil, token)
	router := chi.NewRouter()
	handlers.Mount(router)

	send := func(method, path, bearer, body string) int {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder.Code
	}
	count := func() int {
		var rows int
		if err := harness.pool.QueryRow(ctx,
			`SELECT count(*) FROM audit_events WHERE actor_type = 'operator' AND actor_id = 'operator-token'`,
		).Scan(&rows); err != nil {
			t.Fatalf("count audit rows: %v", err)
		}
		return rows
	}

	// Refused before authentication: nothing to attribute.
	if code := send(http.MethodPost, "/v1/admin/orders", "wrong-token-of-same-length!!!!", "{}"); code != http.StatusUnauthorized {
		t.Fatalf("wrong token answered %d", code)
	}
	// A read changes nothing and is not recorded.
	if code := send(http.MethodGet, "/v1/admin/no-such-route", token, ""); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
		t.Fatalf("unknown GET answered %d", code)
	}
	if rows := count(); rows != 0 {
		t.Fatalf("audit rows before any mutation = %d", rows)
	}

	// A mutation is recorded with its route and outcome, even when it fails.
	if code := send(http.MethodPost, "/v1/admin/orders", token, "not json"); code != http.StatusBadRequest {
		t.Fatalf("malformed order answered %d", code)
	}
	var action, outcome, route string
	if err := harness.pool.QueryRow(ctx,
		`SELECT action, outcome, metadata->>'route' FROM audit_events WHERE actor_id = 'operator-token'`,
	).Scan(&action, &outcome, &route); err != nil {
		t.Fatalf("read audit row: %v", err)
	}
	if action != "admin_api.post" || outcome != "failure" || route != "/v1/admin/orders" {
		t.Fatalf("audit row = %q %q %q", action, outcome, route)
	}
}

// A named token scoped to some permissions reaches the routes they cover and
// is refused the others with 403 — refusals included in its request log. A
// full-access token reaches everything, as named tokens always did.
func TestScopedAPITokenPermissions(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	panel := newOperations(t, harness)
	auth, err := adminauthpg.New(harness.pool, operationsKey, adminauthpg.Options{})
	if err != nil {
		t.Fatal(err)
	}
	admin := apihttp.NewAdminHandlers(apihttp.AdminOptions{
		Service: auth, Operations: panel, Logger: slog.New(slog.DiscardHandler),
	})
	mcpHandler, err := admin.MCPHandler()
	if err != nil {
		t.Fatal(err)
	}
	const shared = "shared-operator-token-for-integration"
	handlers := apihttp.NewCommerceHandlers(dbgen.New(harness.pool), nil, nil, nil, nil, nil, nil, nil, shared).
		WithMachineRoutes(admin.MountMachine).WithMCP(mcpHandler)
	router := chi.NewRouter()
	handlers.Mount(router)
	actor := harness.operator(ctx, t, "tokens@omniflow.test")
	if _, err = harness.pool.Exec(ctx, `INSERT INTO admin_user_roles (admin_user_id, role) VALUES ($1::uuid, 'owner')`, actor.AdminID); err != nil {
		t.Fatal(err)
	}

	if _, err := panel.CreateAPIToken(ctx, "nothing", nil, false, actor); err == nil {
		t.Fatal("a token with no permissions and no full access is refused")
	}
	if _, err := panel.CreateAPIToken(ctx, "bogus", []string{"wallet.everything"}, false, actor); err == nil {
		t.Fatal("a permission outside the catalogue is refused")
	}
	scoped, err := panel.CreateAPIToken(ctx, "support-bot", []string{"support.read", "finance.read"}, false, actor)
	if err != nil || scoped.FullAccess || len(scoped.Permissions) != 2 {
		t.Fatalf("scoped token: %+v %v", scoped, err)
	}
	full, err := panel.CreateAPIToken(ctx, "owner", nil, true, actor)
	if err != nil || !full.FullAccess {
		t.Fatalf("full token: %+v %v", full, err)
	}

	send := func(method, path, bearer, body string) int {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+bearer)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder.Code
	}
	adjust := "/v1/admin/customers/00000000-0000-4000-8000-000000000001/wallet-adjustments"
	if code := send(http.MethodPost, adjust, scoped.Token, `{}`); code != http.StatusForbidden {
		t.Fatalf("a wallet adjustment without finance.write answered %d", code)
	}
	if code := send(http.MethodGet, "/v1/admin/orders/00000000-0000-4000-8000-000000000001", scoped.Token, ""); code == http.StatusForbidden {
		t.Fatal("finance.read reaches an order")
	}
	if code := send(http.MethodPost, adjust, full.Token, `not json`); code == http.StatusForbidden {
		t.Fatal("a full-access token is not refused")
	}
	var refused int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM api_token_requests r JOIN api_tokens t ON t.id = r.token_id
		WHERE t.name = 'support-bot' AND r.status = 403`).Scan(&refused); err != nil || refused != 1 {
		t.Fatalf("refusals in the token log = %d, %v", refused, err)
	}

	// The panel's own handlers, reached by a named token: as the operator who
	// made it, within the token's permissions.
	call := func(method, path, bearer, body string) (int, string) {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+bearer)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder.Code, recorder.Body.String()
	}
	if code, body := call(http.MethodGet, "/v1/admin/support/tickets?updatedSince=2026-01-01T00:00:00Z", scoped.Token, ""); code != http.StatusOK {
		t.Fatalf("support.read lists tickets: %d %s", code, body)
	}
	if code, body := call(http.MethodGet, "/v1/admin/reports/customer-payments?format=csv", scoped.Token, ""); code != http.StatusOK ||
		!strings.HasPrefix(body, "customer_id,currency,payments") {
		t.Fatalf("finance.read exports customer payments: %d %s", code, body)
	}
	if code, _ := call(http.MethodPost, "/v1/admin/marketing/audience/preview", scoped.Token, `{"filters":{}}`); code != http.StatusForbidden {
		t.Fatalf("an audience preview without marketing.read answered %d", code)
	}
	if code, _ := call(http.MethodPost, "/v1/admin/bulk", scoped.Token, `{}`); code != http.StatusForbidden {
		t.Fatalf("a bulk action without customers.write answered %d", code)
	}
	harness.customer(ctx, t)
	if code, body := call(http.MethodPost, "/v1/admin/marketing/audience/preview", full.Token, `{"filters":{}}`); code != http.StatusOK ||
		!strings.Contains(body, `"customerIds":["`) {
		t.Fatalf("a full token previews an audience: %d %s", code, body)
	}
	if code, body := call(http.MethodGet, "/v1/admin/support/tickets", shared, ""); code != http.StatusForbidden ||
		!strings.Contains(body, "named_token_required") {
		t.Fatalf("the shared token names no operator: %d %s", code, body)
	}

	// A token that drafts campaigns cannot send them: starting or scheduling
	// needs marketing.send. Pausing and cancelling stay marketing.write.
	drafts, err := panel.CreateAPIToken(ctx, "drafts", []string{"marketing.read", "marketing.write"}, false, actor)
	if err != nil {
		t.Fatal(err)
	}
	campaignID := seedCampaign(ctx, t, harness, panel, actor)
	state := "/v1/admin/marketing/campaigns/" + campaignID + "/state"
	if code, body := call(http.MethodPost, state, drafts.Token, `{"state":"running"}`); code != http.StatusForbidden {
		t.Fatalf("a drafts token started a campaign: %d %s", code, body)
	}
	if code, body := call(http.MethodPost, state, drafts.Token, `{"state":"cancelled"}`); code != http.StatusOK {
		t.Fatalf("a drafts token cancels a draft: %d %s", code, body)
	}

	// The MCP server: the same token, the same permissions, read-only tools.
	code, body := call(http.MethodPost, "/v1/mcp", scoped.Token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if code != http.StatusOK || !strings.Contains(body, "support_tickets_search") || !strings.Contains(body, "sales_report") ||
		strings.Contains(body, "audience_preview") {
		t.Fatalf("tools/list for a support and finance token: %d %s", code, body)
	}
	code, body = call(http.MethodPost, "/v1/mcp", scoped.Token,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"support_tickets_search","arguments":{"pageSize":5}}}`)
	if code != http.StatusOK || strings.Contains(body, `"error"`) {
		t.Fatalf("tools/call: %d %s", code, body)
	}
	if code, _ = call(http.MethodPost, "/v1/mcp", shared, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`); code != http.StatusUnauthorized {
		t.Fatalf("the shared token on MCP answered %d", code)
	}
	var calls int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = 'mcp.tool_call'`).Scan(&calls); err != nil || calls != 1 {
		t.Fatalf("mcp tool calls in the audit trail = %d, %v", calls, err)
	}

	// A token outlives nobody's access: once its maker is suspended it stops.
	if _, err = harness.pool.Exec(ctx, `UPDATE admin_users SET status = 'suspended' WHERE id = $1::uuid`, actor.AdminID); err != nil {
		t.Fatal(err)
	}
	if code, body := call(http.MethodGet, "/v1/admin/support/tickets", full.Token, ""); code != http.StatusForbidden ||
		!strings.Contains(body, "token_owner_inactive") {
		t.Fatalf("a suspended maker's token: %d %s", code, body)
	}
}
