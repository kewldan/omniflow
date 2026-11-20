package operator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupFileLimitFollowsTheBotAPIServer(t *testing.T) {
	if BackupFileLimit(false) != 50<<20 {
		t.Fatalf("public Bot API limit = %d, want 50 MB", BackupFileLimit(false))
	}
	if BackupFileLimit(true) != 2000<<20 {
		t.Fatalf("self-hosted Bot API limit = %d, want 2000 MB", BackupFileLimit(true))
	}
}

// A file over the limit is refused before anything is uploaded or any client
// is built, so a large backup costs nothing but a log line.
func TestOversizedBackupIsRefusedBeforeUploading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "omniflow-manual.dump.enc")
	if err := os.WriteFile(path, make([]byte, 64), 0o600); err != nil {
		t.Fatal(err)
	}
	err := SendBackupFile(context.Background(), "invalid-token", nil, 32, 1, 0, "omniflow-manual.dump.enc", path)
	if !errors.Is(err, ErrBackupTooLarge) {
		t.Fatalf("got %v, want ErrBackupTooLarge", err)
	}
}
