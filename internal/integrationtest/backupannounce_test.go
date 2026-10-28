//go:build integration

package integrationtest

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/omniflow/omniflow/internal/backup"
)

// TestABackupOutcomeReachesTheOperatorQueue covers the backups topic: the
// worker that takes a backup has no Telegram client, so its outcome is queued
// for the bot. A failed dump is the outcome a test can produce without a
// pg_dump matching the server, and it is the one an operator most needs to see.
func TestABackupOutcomeReachesTheOperatorQueue(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := backup.New(harness.pool, slog.New(slog.DiscardHandler), backup.Config{
		Enabled: true, Directory: t.TempDir(), EncryptionKey: make([]byte, 32),
		PgDumpPath: "/nonexistent/pg_dump", DatabaseURL: harness.url,
	})
	if _, err := service.Create(ctx, "manual", ""); err == nil {
		t.Fatal("a backup with no pg_dump succeeded")
	}

	var raw []byte
	if err := harness.pool.QueryRow(ctx, `SELECT payload FROM operator_notifications
		WHERE kind = 'backup' AND dedupe_key LIKE 'backup:%:failed'`).Scan(&raw); err != nil {
		t.Fatalf("read backup notice: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode notice: %v", err)
	}
	if payload["status"] != "failed" || payload["source"] != "manual" || payload["fileName"] == "" {
		t.Fatalf("backup notice payload = %v", payload)
	}
}
