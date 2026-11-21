//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/omniflow/omniflow/internal/panelpg"
)

// A named token is shown once and resolvable by hash until revoked; an
// outbox event reaches a subscribed webhook signed, once, and the private
// address a webhook may not point at is refused.
func TestAPITokensAndWebhooks(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	service, err := panelpg.New(harness.pool, operationsKey, panelpg.Options{})
	if err != nil {
		t.Fatal(err)
	}
	actor := harness.operator(ctx, t, "integrations@example.test")

	token, err := service.CreateAPIToken(ctx, "CRM", nil, true, actor)
	if err != nil || len(token.Token) < 20 {
		t.Fatalf("token = %+v, %v", token, err)
	}
	listed, _ := service.APITokens(ctx)
	if len(listed) != 1 || listed[0].Token != "" {
		t.Fatal("a listed token must not carry its value")
	}
	var resolved int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM api_tokens WHERE token_hash = $1 AND revoked_at IS NULL`, panelpg.HashAPIToken(token.Token)).Scan(&resolved); err != nil || resolved != 1 {
		t.Fatalf("lookup by hash = %d, %v", resolved, err)
	}
	if err = service.RevokeAPIToken(ctx, token.ID, actor); err != nil {
		t.Fatal(err)
	}

	if _, err = service.CreateWebhook(ctx, panelpg.WebhookInput{Name: "Internal", URL: "https://127.0.0.1/hook", Events: []string{"order.paid"}}, actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("a private address: %v", err)
	}
	created, err := service.CreateWebhook(ctx, panelpg.WebhookInput{Name: "CRM", URL: "https://example.com/hook", Events: []string{"order.paid"}}, actor)
	if err != nil || created.Secret == "" {
		t.Fatalf("webhook = %+v, %v", created, err)
	}
	received := make(chan *http.Request, 4)
	bodies := make(chan []byte, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		received <- request
		bodies <- body
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	// The test server is local, which the egress policy rightly refuses, so
	// the stored address is pointed at it directly.
	if _, err = harness.pool.Exec(ctx, `UPDATE webhook_endpoints SET url = $1 WHERE id = $2::uuid`, server.URL, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = harness.pool.Exec(ctx, `INSERT INTO outbox_events (topic, payload) VALUES ('order.paid', '{"orderId":"x"}'), ('wallet.credited', '{}')`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = service.DispatchWebhooks(ctx, server.Client()); err != nil {
			t.Fatal(err)
		}
	}
	if len(received) != 1 {
		t.Fatalf("deliveries = %d, want one subscribed event once", len(received))
	}
	request, body := <-received, <-bodies
	stamp, _ := strconv.ParseInt(request.Header.Get("X-Omniflow-Timestamp"), 10, 64)
	if request.Header.Get("X-Omniflow-Signature") != "sha256="+panelpg.SignWebhook(created.Secret, stamp, body) {
		t.Fatal("the signature must verify with the secret shown at creation")
	}
	deliveries, err := service.WebhookDeliveries(ctx, created.ID)
	if err != nil || len(deliveries) != 1 || deliveries[0].Status != "delivered" {
		t.Fatalf("deliveries = %+v, %v", deliveries, err)
	}
}
