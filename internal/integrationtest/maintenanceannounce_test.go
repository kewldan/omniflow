//go:build integration

package integrationtest

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/operator"
)

// TestAMaintenanceTransitionReachesTheOperatorQueue covers the bridge between
// the api process, which detects maintenance, and the bot, which owns the
// operator group: a transition becomes one queued incident notice, and a
// repeat of the same transition inside the same minute does not add another.
func TestAMaintenanceTransitionReachesTheOperatorQueue(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	announcer := operator.NewQueuedAnnouncer(harness.pool, slog.New(slog.DiscardHandler))

	state := commerce.Maintenance{Active: true, Source: "automatic", Reason: "remnawave unreachable"}
	announcer.MaintenanceChanged(ctx, state)
	announcer.MaintenanceChanged(ctx, state)

	rows, err := harness.pool.Query(ctx, `SELECT payload FROM operator_notifications
		WHERE kind = 'incident' AND dedupe_key LIKE 'maintenance:activated:%'`)
	if err != nil {
		t.Fatalf("read notices: %v", err)
	}
	defer rows.Close()
	var payloads []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatalf("scan notice: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("decode notice: %v", err)
		}
		payloads = append(payloads, payload)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read notices: %v", err)
	}
	if len(payloads) != 1 {
		t.Fatalf("queued %d maintenance notices, want 1", len(payloads))
	}
	if payloads[0]["status"] != "maintenance_activated" || payloads[0]["source"] != "automatic" {
		t.Fatalf("notice payload = %v", payloads[0])
	}
}
