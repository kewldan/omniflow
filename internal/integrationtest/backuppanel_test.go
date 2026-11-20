//go:build integration

package integrationtest

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/backup"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// An uploaded file is stored only when it opens with the installation's key;
// the panel's requests are validated, audited, and carried out by the worker.
func TestBackupsFromThePanel(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	key := bytes.Repeat([]byte{7}, 32)
	directory := t.TempDir()
	service := backup.New(harness.pool, slog.New(slog.DiscardHandler), backup.Config{Enabled: true, Directory: directory, EncryptionKey: key})

	if _, err := service.Import(ctx, strings.NewReader("not a backup"), "panel:test"); !errors.Is(err, backup.ErrUnreadableUpload) {
		t.Fatalf("a foreign file: %v", err)
	}
	encrypted := &bytes.Buffer{}
	if err := backup.Encrypt(encrypted, strings.NewReader("PGDMP fake dump"), key); err != nil {
		t.Fatal(err)
	}
	record, err := service.Import(ctx, encrypted, "panel:test")
	if err != nil || record.Kind != "uploaded" || record.Status != "completed" {
		t.Fatalf("upload = %+v, %v", record, err)
	}
	if _, err = os.Stat(service.Path(record)); err != nil {
		t.Fatalf("stored file: %v", err)
	}

	panel, err := panelpg.New(harness.pool, operationsKey, panelpg.Options{})
	if err != nil {
		t.Fatal(err)
	}
	actor := harness.operator(ctx, t, "backups@example.test")
	backupID := uuidText(record.ID)
	if _, err = panel.RequestBackupAction(ctx, panelpg.BackupRequestInput{Action: "restore", BackupID: backupID}, actor); !errors.Is(err, panelpg.ErrValidaton) {
		t.Fatalf("a restore without a reason: %v", err)
	}
	if _, err = panel.RequestBackupAction(ctx, panelpg.BackupRequestInput{Action: "delete", BackupID: backupID}, actor); err != nil {
		t.Fatal(err)
	}
	done, err := service.ProcessRequest(ctx)
	if !done || err != nil {
		t.Fatalf("process = %v, %v", done, err)
	}
	if _, err = os.Stat(service.Path(record)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted file still present: %v", err)
	}
	requests, err := panel.BackupRequests(ctx)
	if err != nil || len(requests) != 1 || requests[0].Status != "completed" {
		t.Fatalf("requests = %+v, %v", requests, err)
	}
}
