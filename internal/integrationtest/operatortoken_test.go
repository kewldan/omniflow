//go:build integration

package integrationtest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
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
