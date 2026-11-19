package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestConnectionEnvironmentKeepsThePasswordOffTheCommandLine(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want []string
		err  bool
	}{
		{
			name: "full URL",
			url:  "postgres://omniflow:s3cret@db.internal:6432/omniflow?sslmode=verify-full",
			want: []string{
				"PGHOST=db.internal", "PGPORT=6432", "PGDATABASE=omniflow",
				"PGUSER=omniflow", "PGPASSWORD=s3cret", "PGSSLMODE=verify-full",
			},
		},
		{
			name: "default port and no TLS mode",
			url:  "postgresql://omniflow@db/omniflow",
			want: []string{"PGHOST=db", "PGPORT=5432", "PGDATABASE=omniflow", "PGUSER=omniflow"},
		},
		{
			// A password with reserved characters arrives percent-encoded and
			// must reach libpq decoded, or every backup fails to authenticate.
			name: "percent-encoded password",
			url:  "postgres://omniflow:p%40ss%3Aw%2Frd@db:5432/omniflow",
			want: []string{"PGUSER=omniflow", "PGPASSWORD=p@ss:w/rd"},
		},
		{
			name: "IPv6 host",
			url:  "postgres://omniflow:x@[::1]:5433/omniflow",
			want: []string{"PGHOST=::1", "PGPORT=5433", "PGPASSWORD=x"},
		},
		{name: "no host", url: "postgres:///omniflow", err: true},
		{name: "not a URL", url: "host=db user=omniflow", err: true},
		{name: "empty", url: "", err: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			environment, err := connectionEnvironment(testCase.url)
			if testCase.err {
				if err == nil {
					t.Fatalf("connectionEnvironment(%q) = %v, want an error", testCase.url, environment)
				}
				// The refusal must not echo the URL, which may carry a password.
				if strings.Contains(err.Error(), testCase.url) && testCase.url != "" {
					t.Fatalf("the error echoes the database URL: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("connectionEnvironment: %v", err)
			}
			for _, want := range testCase.want {
				if !slices.Contains(environment, want) {
					t.Fatalf("environment %v lacks %q", environment, want)
				}
			}
			hasPassword := slices.ContainsFunc(environment, func(entry string) bool {
				return strings.HasPrefix(entry, "PGPASSWORD=")
			})
			if wantPassword := slices.ContainsFunc(testCase.want, func(entry string) bool {
				return strings.HasPrefix(entry, "PGPASSWORD=")
			}); hasPassword != wantPassword {
				t.Fatalf("PGPASSWORD present = %v, want %v", hasPassword, wantPassword)
			}
			for _, entry := range environment {
				if !strings.HasPrefix(entry, "PG") && !strings.HasPrefix(entry, "PATH=") {
					t.Fatalf("unexpected variable %q leaks into the child", entry)
				}
			}
		})
	}
}

func TestClassifyReducesFailuresToStableCodes(t *testing.T) {
	exitErr := exec.Command("sh", "-c", "exit 3").Run()
	if runtime.GOOS == "windows" {
		exitErr = nil
	}
	_, missing := os.Open(filepath.Join(t.TempDir(), "missing"))
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"corrupt", ErrCorrupt, "verification_failed"},
		{"bad format", fmt.Errorf("verify: %w", ErrBadFormat), "verification_failed"},
		{"missing file", missing, "file_missing"},
		{"cancelled", context.Canceled, "cancelled"},
		{"timed out", fmt.Errorf("pg_dump: %w", context.DeadlineExceeded), "cancelled"},
		// Command output routinely names hosts and roles; none of it survives.
		{"anything else", errors.New("connection to db.internal as omniflow failed"), "backup_failed"},
	}
	if exitErr != nil {
		cases = append(cases, struct {
			name string
			err  error
			want string
		}{"command exit", fmt.Errorf("pg_dump failed: %w", exitErr), "command_exit_3"})
	}
	for _, testCase := range cases {
		code := classify(testCase.err)
		if !code.Valid || code.String != testCase.want {
			t.Fatalf("%s: classify = %+v, want %q", testCase.name, code, testCase.want)
		}
	}
}

func TestNewAppliesSafeDefaultsAndEnabledNeedsA256BitKey(t *testing.T) {
	service := New(nil, slog.New(slog.DiscardHandler), Config{Enabled: true})
	if service.config.PgDumpPath != "pg_dump" || service.config.PgRestorePath != "pg_restore" {
		t.Fatalf("tool defaults = %q, %q", service.config.PgDumpPath, service.config.PgRestorePath)
	}
	if service.config.Interval != 24*time.Hour || service.config.Retention != 14*24*time.Hour {
		t.Fatalf("schedule defaults = %v, %v", service.config.Interval, service.config.Retention)
	}

	var nilService *Service
	cases := []struct {
		name    string
		service *Service
		want    bool
	}{
		{"nil service", nilService, false},
		{"disabled with a key", New(nil, nil, Config{EncryptionKey: make([]byte, 32)}), false},
		{"enabled with no key", New(nil, nil, Config{Enabled: true}), false},
		{"enabled with a 128-bit key", New(nil, nil, Config{Enabled: true, EncryptionKey: make([]byte, 16)}), false},
		{"enabled with a 256-bit key", New(nil, nil, Config{Enabled: true, EncryptionKey: make([]byte, 32)}), true},
	}
	for _, testCase := range cases {
		if got := testCase.service.Enabled(); got != testCase.want {
			t.Fatalf("%s: Enabled = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

// Every refusal below happens before the database is touched, which is why a
// nil pool is safe: reaching it would panic and fail the test.
func TestRefusalsHappenBeforeAnyDatabaseWork(t *testing.T) {
	ctx := context.Background()
	disabled := New(nil, slog.New(slog.DiscardHandler), Config{})
	if _, err := disabled.Create(ctx, "manual", "operator"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Create on a disabled service = %v, want ErrDisabled", err)
	}
	if _, err := disabled.Restore(ctx, "id", "operator", "reason"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Restore on a disabled service = %v, want ErrDisabled", err)
	}

	enabled := New(nil, slog.New(slog.DiscardHandler), Config{Enabled: true, EncryptionKey: make([]byte, 32)})
	for _, testCase := range []struct{ operator, reason string }{{"", "reason"}, {"operator", ""}, {"", ""}} {
		if _, err := enabled.Restore(ctx, "id", testCase.operator, testCase.reason); err == nil {
			t.Fatalf("Restore(%q, %q) proceeded without an operator and a reason", testCase.operator, testCase.reason)
		}
	}
	for _, id := range []string{"", "not-a-uuid", "../../etc/passwd", "1; DROP TABLE backups"} {
		if err := enabled.Verify(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Verify(%q) = %v, want ErrNotFound", id, err)
		}
		if _, err := enabled.Restore(ctx, id, "operator", "reason"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Restore(%q) = %v, want ErrNotFound", id, err)
		}
	}
}

// fakeTool writes an executable shell script standing in for pg_dump or
// pg_restore. The child runs with only the libpq variables and PATH, so the
// script records its argv and environment into files whose paths are baked
// into its text.
func fakeTool(t *testing.T, directory, name, body string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > '" + path + ".args'\n" +
		"env > '" + path + ".env'\n" +
		body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

// TestDumpVerifyAndRestoreRoundTripThroughTheRealTools drives the private
// pipeline with stand-in binaries: what pg_dump writes is what pg_restore
// reads back, the file on disk is encrypted and digested, and the password
// travels in the environment rather than in argv.
func TestDumpVerifyAndRestoreRoundTripThroughTheRealTools(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in tools are POSIX shell scripts")
	}
	directory := t.TempDir()
	// Larger than two frames, so frame sequencing is exercised end to end.
	plaintext := make([]byte, 2*frameSize+12345)
	if _, err := rand.Read(plaintext); err != nil {
		t.Fatalf("rand: %v", err)
	}
	source := filepath.Join(directory, "dump.bin")
	if err := os.WriteFile(source, plaintext, 0o600); err != nil {
		t.Fatalf("write dump: %v", err)
	}
	restored := filepath.Join(directory, "restored.bin")
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	const password = "Sup3r-s3cret"
	service := New(nil, slog.New(slog.DiscardHandler), Config{
		Enabled: true, Directory: directory, EncryptionKey: key,
		PgDumpPath:    fakeTool(t, directory, "pg_dump", "cat '"+source+"'"),
		PgRestorePath: fakeTool(t, directory, "pg_restore", "cat > '"+restored+"'"),
		DatabaseURL:   "postgres://omniflow:" + password + "@127.0.0.1:5432/omniflow?sslmode=disable",
	})
	ctx := context.Background()

	backupPath := filepath.Join(directory, "backup.dump.enc")
	size, digest, err := service.dump(ctx, backupPath)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	encrypted, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if int64(len(encrypted)) != size {
		t.Fatalf("reported size %d, file is %d bytes", size, len(encrypted))
	}
	if sum := sha256.Sum256(encrypted); !bytes.Equal(sum[:], digest) {
		t.Fatal("the recorded digest is not the digest of the file on disk")
	}
	if bytes.Contains(encrypted, plaintext[:64]) {
		t.Fatal("the backup file carries plaintext")
	}
	if info, statErr := os.Stat(backupPath); statErr != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup file mode = %v, %v; want 0600", info.Mode().Perm(), statErr)
	}

	dumpArgs := readFile(t, service.config.PgDumpPath+".args")
	dumpEnv := readFile(t, service.config.PgDumpPath+".env")
	if strings.Contains(dumpArgs, password) {
		t.Fatal("the database password reached pg_dump's argv")
	}
	if !strings.Contains(dumpEnv, "PGPASSWORD="+password) || !strings.Contains(dumpEnv, "PGSSLMODE=disable") {
		t.Fatal("pg_dump did not receive the connection through libpq variables")
	}
	if !strings.Contains(dumpArgs, "--format=custom") {
		t.Fatalf("pg_dump args = %q; a custom-format dump is what pg_restore reads", dumpArgs)
	}

	if err = service.verifyFile(backupPath, digest); err != nil {
		t.Fatalf("verifyFile: %v", err)
	}
	wrongDigest := slices.Clone(digest)
	wrongDigest[0] ^= 0xff
	if err = service.verifyFile(backupPath, wrongDigest); err == nil {
		t.Fatal("verifyFile accepted a digest the file does not have")
	}

	if err = service.runRestore(ctx, backupPath); err != nil {
		t.Fatalf("runRestore: %v", err)
	}
	if got, readErr := os.ReadFile(restored); readErr != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("pg_restore received %d bytes (%v), want the original %d", len(got), readErr, len(plaintext))
	}
	restoreArgs := readFile(t, service.config.PgRestorePath+".args")
	// A restore that fails part way must not leave a half-replaced database.
	for _, flag := range []string{"--single-transaction", "--clean", "--if-exists"} {
		if !strings.Contains(restoreArgs, flag+"\n") {
			t.Fatalf("pg_restore args %q lack %s", restoreArgs, flag)
		}
	}
	if strings.Contains(restoreArgs, password) {
		t.Fatal("the database password reached pg_restore's argv")
	}

	// A tampered byte fails both verification and restore.
	encrypted[len(encrypted)/2] ^= 0x01
	if err = os.WriteFile(backupPath, encrypted, 0o600); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if err = service.verifyFile(backupPath, digest); err == nil {
		t.Fatal("verifyFile accepted a tampered backup")
	}
	if err = service.runRestore(ctx, backupPath); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("runRestore of a tampered backup = %v, want ErrCorrupt", err)
	}
}

func TestDumpFailuresAreReportedWithoutTheToolsOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in tools are POSIX shell scripts")
	}
	directory := t.TempDir()
	service := New(nil, slog.New(slog.DiscardHandler), Config{
		Enabled: true, Directory: directory, EncryptionKey: make([]byte, 32),
		PgDumpPath: fakeTool(t, directory, "pg_dump",
			"echo 'FATAL: password authentication failed for user omniflow at db.internal' >&2; exit 2"),
		DatabaseURL: "postgres://omniflow:pw@db.internal/omniflow",
	})
	ctx := context.Background()

	_, _, err := service.dump(ctx, filepath.Join(directory, "failed.dump.enc"))
	if err == nil {
		t.Fatal("a failing pg_dump produced a backup")
	}
	if strings.Contains(err.Error(), "db.internal") || strings.Contains(err.Error(), "FATAL") {
		t.Fatalf("the error carries pg_dump's diagnostics: %v", err)
	}
	if code := classify(err); code.String != "command_exit_2" {
		t.Fatalf("classify = %q, want command_exit_2", code.String)
	}

	// An existing file is never overwritten: a backup name collision must not
	// destroy the backup already there.
	existing := filepath.Join(directory, "existing.dump.enc")
	if err = os.WriteFile(existing, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("write existing: %v", err)
	}
	service.config.PgDumpPath = fakeTool(t, directory, "pg_dump_ok", "printf 'data'")
	if _, _, err = service.dump(ctx, existing); err == nil {
		t.Fatal("dump overwrote an existing backup file")
	}
	if readFile(t, existing) != "keep me" {
		t.Fatal("the existing backup was modified")
	}

	// A database URL that cannot be turned into libpq variables stops the dump
	// before any tool runs.
	service.config.DatabaseURL = "not a url"
	if _, _, err = service.dump(ctx, filepath.Join(directory, "never.dump.enc")); err == nil {
		t.Fatal("dump ran without a usable database URL")
	}
}
