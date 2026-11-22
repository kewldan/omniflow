//go:build integration

package integrationtest

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/omniflow/omniflow/internal/remnawavesync"
)

func signRemnawave(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func (harness *harness) webhookStatus(ctx context.Context, t *testing.T, event string) string {
	t.Helper()
	var status string
	if err := harness.pool.QueryRow(ctx, `SELECT status FROM remnawave_webhook_events
		WHERE event = $1 ORDER BY id DESC LIMIT 1`, event).Scan(&status); err != nil {
		t.Fatalf("read webhook event %s: %v", event, err)
	}
	return status
}

// A signed panel event about a known subscription becomes exactly one notice
// to its customer; a retried delivery, an unsigned one, and an event about a
// panel user Omniflow does not know produce nothing. Node events reach the
// operator queue in the same transaction as their record.
func TestRemnawaveWebhooksNotifyCustomersAndOperators(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	const telegramID = 710001
	customer := flow.customerOf(ctx, t, telegramID)
	subscription := harness.seedSubscription(ctx, t, customer, 1)
	if _, err := harness.pool.Exec(ctx, `UPDATE subscriptions SET remnawave_user_id = 555 WHERE id = $1::uuid`, subscription); err != nil {
		t.Fatal(err)
	}
	intake := remnawavesync.NewIntake(harness.pool, remnawavesync.IntakeOptions{Secret: "panel-secret", NodeAlerts: true})

	connected := `{"scope":"user","event":"user.first_connected","data":{"id":555,"subscriptionUrl":"https://sub.example/x"}}`
	if _, err := intake.Accept(ctx, []byte(connected), "deadbeef"); !errors.Is(err, remnawavesync.ErrBadSignature) {
		t.Fatalf("an unsigned delivery gave %v", err)
	}
	outcome, err := intake.Accept(ctx, []byte(connected), signRemnawave("panel-secret", connected))
	if err != nil || !outcome.Customer || outcome.Duplicate {
		t.Fatalf("first delivery: %+v %v", outcome, err)
	}
	if again, err := intake.Accept(ctx, []byte(connected), signRemnawave("panel-secret", connected)); err != nil || !again.Duplicate {
		t.Fatalf("a retried delivery was recorded twice: %+v %v", again, err)
	}
	var stored string
	if err := harness.pool.QueryRow(ctx, `SELECT details::text FROM remnawave_webhook_events WHERE event = 'user.first_connected'`).Scan(&stored); err != nil || stored != "{}" {
		t.Fatalf("details = %q (%v); the access link must not be stored", stored, err)
	}
	// The first connection is also the activation figure business analytics
	// reports, recorded once whatever the retries.
	var firstConnections int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*) FROM vpn_usage_observations
		WHERE remnawave_user_id = 555 AND first_connected_at IS NOT NULL`).Scan(&firstConnections); err != nil || firstConnections != 1 {
		t.Fatalf("first connections recorded = %d (%v), want 1", firstConnections, err)
	}

	stranger := `{"event":"user.not_connected","data":{"id":999}}`
	if _, err := intake.Accept(ctx, []byte(stranger), signRemnawave("panel-secret", stranger)); err != nil {
		t.Fatal(err)
	}
	node := `{"event":"node.connection_lost","data":{"id":4,"name":"Frankfurt 1"}}`
	if outcome, err := intake.Accept(ctx, []byte(node), signRemnawave("panel-secret", node)); err != nil || !outcome.Operator {
		t.Fatalf("node event: %+v %v", outcome, err)
	}
	var payload string
	if err := harness.pool.QueryRow(ctx, `SELECT payload::text FROM operator_notifications WHERE kind = 'node'`).Scan(&payload); err != nil {
		t.Fatalf("no operator notice for the node: %v", err)
	}

	flow.notifier().DeliverPanelEvents(ctx)
	if sent := flow.telegram.sends(telegramID); sent != 1 {
		status, code := deliveryRow(ctx, t, harness, customer, "connection", "panel:1")
		t.Fatalf("the customer got %d messages, want 1 (delivery %s %s)", sent, status, code)
	}
	if status := harness.webhookStatus(ctx, t, "user.first_connected"); status != "delivered" {
		t.Fatalf("first_connected is %s", status)
	}
	if status := harness.webhookStatus(ctx, t, "user.not_connected"); status != "ignored" {
		t.Fatalf("an unknown panel user's event is %s, want ignored", status)
	}
	if status := harness.webhookStatus(ctx, t, "node.connection_lost"); status != "delivered" {
		t.Fatalf("node event is %s", status)
	}
	flow.notifier().DeliverPanelEvents(ctx)
	if sent := flow.telegram.sends(telegramID); sent != 1 {
		t.Fatalf("a second pass sent again: %d", sent)
	}
}
