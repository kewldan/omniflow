// Package backup takes, verifies, prunes, and restores encrypted PostgreSQL
// backups. It shells out to pg_dump and pg_restore rather than reimplementing
// them, so a backup is always restorable with standard PostgreSQL tooling.
package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Config is the operator's backup policy.
type Config struct {
	Enabled       bool
	Directory     string
	Interval      time.Duration
	Retention     time.Duration
	EncryptionKey []byte
	PgDumpPath    string
	PgRestorePath string
	// DatabaseURL is the connection Omniflow dumps and restores.
	DatabaseURL string
}

var (
	ErrDisabled     = errors.New("backups are not configured")
	ErrNotFound     = errors.New("backup not found")
	ErrNotRestorabl = errors.New("backup is not restorable")
	ErrBusy         = errors.New("another backup is already running")
)

// Service owns the backup lifecycle.
type Service struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
	config Config
	clock  func() time.Time
}

func New(pool *pgxpool.Pool, logger *slog.Logger, config Config) *Service {
	if config.PgDumpPath == "" {
		config.PgDumpPath = "pg_dump"
	}
	if config.PgRestorePath == "" {
		config.PgRestorePath = "pg_restore"
	}
	if config.Interval <= 0 {
		config.Interval = 24 * time.Hour
	}
	if config.Retention <= 0 {
		config.Retention = 14 * 24 * time.Hour
	}
	return &Service{pool: pool, logger: logger, config: config, clock: time.Now}
}

// Enabled reports whether the operator configured backups.
func (service *Service) Enabled() bool {
	return service != nil && service.config.Enabled && len(service.config.EncryptionKey) == 32
}

// Run takes a scheduled backup on the configured interval and prunes expired
// ones after each run.
func (service *Service) Run(ctx context.Context) {
	if !service.Enabled() {
		service.logger.Info("scheduled backups are disabled")
		return
	}
	ticker := time.NewTicker(service.config.Interval)
	defer ticker.Stop()
	for {
		if _, err := service.Create(ctx, "scheduled", "scheduler"); err != nil && !errors.Is(err, ErrBusy) {
			service.logger.Error("scheduled backup failed", "error", err)
		}
		if err := service.Prune(ctx); err != nil {
			service.logger.Warn("backup pruning failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Create takes one backup: pg_dump in PostgreSQL's custom format, encrypted
// with AES-256-GCM, written to the backup directory, then verified by re-reading
// the file and checking both its SHA-256 digest and that every frame opens.
func (service *Service) Create(ctx context.Context, kind, requestedBy string) (dbgen.Backup, error) {
	if !service.Enabled() {
		return dbgen.Backup{}, ErrDisabled
	}
	queries := dbgen.New(service.pool)
	running, err := queries.CountRunningBackups(ctx)
	if err != nil {
		return dbgen.Backup{}, err
	}
	if running > 0 {
		return dbgen.Backup{}, ErrBusy
	}
	if err = os.MkdirAll(service.config.Directory, 0o700); err != nil {
		return dbgen.Backup{}, err
	}
	fileName := fmt.Sprintf("omniflow-%s-%s.dump.enc", kind, service.clock().UTC().Format("20060102T150405Z"))
	record, err := queries.CreateBackup(ctx, dbgen.CreateBackupParams{
		Kind: kind, FileName: fileName, Encrypted: true,
		RequestedBy: pgtype.Text{String: requestedBy, Valid: requestedBy != ""},
		RetainUntil: pgtype.Timestamptz{Time: service.clock().Add(service.config.Retention), Valid: true},
	})
	if err != nil {
		return dbgen.Backup{}, err
	}
	path := filepath.Join(service.config.Directory, fileName)
	size, digest, dumpErr := service.dump(ctx, path)
	if dumpErr != nil {
		_ = os.Remove(path)
		if _, failErr := queries.FailBackup(ctx, dbgen.FailBackupParams{BackupID: record.ID, ErrorCode: classify(dumpErr)}); failErr != nil {
			return dbgen.Backup{}, failErr
		}
		return dbgen.Backup{}, dumpErr
	}
	if verifyErr := service.verifyFile(path, digest); verifyErr != nil {
		_ = os.Remove(path)
		if _, failErr := queries.FailBackup(ctx, dbgen.FailBackupParams{BackupID: record.ID, ErrorCode: pgtype.Text{String: "verification_failed", Valid: true}}); failErr != nil {
			return dbgen.Backup{}, failErr
		}
		return dbgen.Backup{}, verifyErr
	}
	completed, err := queries.CompleteBackup(ctx, dbgen.CompleteBackupParams{BackupID: record.ID, SizeBytes: size, Sha256: digest})
	if err != nil {
		return dbgen.Backup{}, err
	}
	service.logger.Info("backup completed", "kind", kind, "size_bytes", size, "file", fileName)
	return completed, nil
}

// dump runs pg_dump and streams its output through the encrypter.
func (service *Service) dump(ctx context.Context, path string) (int64, []byte, error) {
	command := exec.CommandContext(ctx, service.config.PgDumpPath, "--format=custom", "--no-owner", "--no-privileges", "--compress=6")
	environment, err := connectionEnvironment(service.config.DatabaseURL)
	if err != nil {
		return 0, nil, err
	}
	command.Env = environment
	stderr := &bytes.Buffer{}
	command.Stderr = stderr
	output, err := command.StdoutPipe()
	if err != nil {
		return 0, nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, nil, err
	}
	defer file.Close()
	digest := sha256.New()
	counter := &countingWriter{}
	if err = command.Start(); err != nil {
		return 0, nil, err
	}
	encryptErr := Encrypt(io.MultiWriter(file, digest, counter), output, service.config.EncryptionKey)
	waitErr := command.Wait()
	if waitErr != nil {
		// pg_dump's own diagnostics can name hosts and roles, so only the exit
		// status is kept.
		return 0, nil, fmt.Errorf("pg_dump failed: %w", waitErr)
	}
	if encryptErr != nil {
		return 0, nil, encryptErr
	}
	if err = file.Sync(); err != nil {
		return 0, nil, err
	}
	return counter.total, digest.Sum(nil), nil
}

// verifyFile re-reads a written backup, checks its digest, and opens every
// frame. A backup that cannot be decrypted is never recorded as completed.
func (service *Service) verifyFile(path string, expected []byte) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	digest := sha256.New()
	if err = Decrypt(io.Discard, io.TeeReader(file, digest), service.config.EncryptionKey); err != nil {
		return err
	}
	if !bytes.Equal(digest.Sum(nil), expected) {
		return errors.New("backup digest mismatch")
	}
	return nil
}

// Verify re-checks a stored backup on demand.
func (service *Service) Verify(ctx context.Context, backupID string) error {
	record, err := service.get(ctx, backupID)
	if err != nil {
		return err
	}
	if record.Status != "completed" {
		return ErrNotRestorabl
	}
	return service.verifyFile(filepath.Join(service.config.Directory, record.FileName), record.Sha256)
}

// Restore replays a verified backup over the live database. It is deliberately
// blunt: pg_restore runs with --clean --if-exists, so the caller must have
// confirmed the destructive intent and recorded who asked for it.
func (service *Service) Restore(ctx context.Context, backupID, operatorID, reason string) (dbgen.BackupRestore, error) {
	if !service.Enabled() {
		return dbgen.BackupRestore{}, ErrDisabled
	}
	if operatorID == "" || reason == "" {
		return dbgen.BackupRestore{}, errors.New("a restore requires an operator and a reason")
	}
	record, err := service.get(ctx, backupID)
	if err != nil {
		return dbgen.BackupRestore{}, err
	}
	if record.Status != "completed" {
		return dbgen.BackupRestore{}, ErrNotRestorabl
	}
	path := filepath.Join(service.config.Directory, record.FileName)
	if err = service.verifyFile(path, record.Sha256); err != nil {
		return dbgen.BackupRestore{}, err
	}
	queries := dbgen.New(service.pool)
	restore, err := queries.CreateBackupRestore(ctx, dbgen.CreateBackupRestoreParams{BackupID: record.ID, OperatorID: operatorID, Reason: reason})
	if err != nil {
		return dbgen.BackupRestore{}, err
	}
	restoreErr := service.runRestore(ctx, path)
	status, errorCode := "completed", pgtype.Text{}
	if restoreErr != nil {
		status, errorCode = "failed", classify(restoreErr)
		service.logger.Error("restore failed", "backup", record.FileName, "error", restoreErr)
	}
	completed, err := queries.CompleteBackupRestore(ctx, dbgen.CompleteBackupRestoreParams{RestoreID: restore.ID, Status: status, ErrorCode: errorCode})
	if err != nil {
		return dbgen.BackupRestore{}, err
	}
	return completed, restoreErr
}

func (service *Service) runRestore(ctx context.Context, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	command := exec.CommandContext(ctx, service.config.PgRestorePath, "--clean", "--if-exists", "--no-owner", "--no-privileges", "--single-transaction")
	environment, err := connectionEnvironment(service.config.DatabaseURL)
	if err != nil {
		return err
	}
	command.Env = environment
	stdin, err := command.StdinPipe()
	if err != nil {
		return err
	}
	stderr := &bytes.Buffer{}
	command.Stderr = stderr
	if err = command.Start(); err != nil {
		return err
	}
	decryptErr := Decrypt(stdin, file, service.config.EncryptionKey)
	closeErr := stdin.Close()
	waitErr := command.Wait()
	switch {
	case decryptErr != nil:
		return decryptErr
	case waitErr != nil:
		return fmt.Errorf("pg_restore failed: %w", waitErr)
	default:
		return closeErr
	}
}

// Prune deletes backups whose retention window elapsed. The database row is kept
// and marked pruned, so the history of what existed remains auditable.
func (service *Service) Prune(ctx context.Context) error {
	queries := dbgen.New(service.pool)
	expired, err := queries.ListExpiredBackups(ctx, 100)
	if err != nil {
		return err
	}
	for _, record := range expired {
		path := filepath.Join(service.config.Directory, record.FileName)
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			service.logger.Warn("backup file could not be removed", "error", removeErr)
			continue
		}
		if _, markErr := queries.MarkBackupPruned(ctx, record.ID); markErr != nil {
			return markErr
		}
	}
	return nil
}

// List returns the most recent backups for an operator status view.
func (service *Service) List(ctx context.Context, limit int32) ([]dbgen.Backup, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	return dbgen.New(service.pool).ListBackups(ctx, limit)
}

// Latest returns the newest completed backup, if any.
func (service *Service) Latest(ctx context.Context) (dbgen.Backup, bool, error) {
	record, err := dbgen.New(service.pool).GetLatestBackup(ctx)
	if err != nil {
		return dbgen.Backup{}, false, nil
	}
	return record, true, nil
}

func (service *Service) get(ctx context.Context, backupID string) (dbgen.Backup, error) {
	var id pgtype.UUID
	if err := id.Scan(backupID); err != nil || !id.Valid {
		return dbgen.Backup{}, ErrNotFound
	}
	record, err := dbgen.New(service.pool).GetBackup(ctx, id)
	if err != nil {
		return dbgen.Backup{}, ErrNotFound
	}
	return record, nil
}

// connectionEnvironment turns the database URL into libpq environment variables.
// The password never reaches the child's argv, where any local user could read
// it from the process list.
func connectionEnvironment(databaseURL string) ([]string, error) {
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.Host == "" {
		return nil, errors.New("a valid APP_DATABASE_URL is required for backups")
	}
	host, port := parsed.Hostname(), parsed.Port()
	if port == "" {
		port = "5432"
	}
	environment := []string{
		"PGHOST=" + host,
		"PGPORT=" + port,
		"PGDATABASE=" + strings.TrimPrefix(parsed.Path, "/"),
		"PATH=" + os.Getenv("PATH"),
	}
	if parsed.User != nil {
		environment = append(environment, "PGUSER="+parsed.User.Username())
		if password, ok := parsed.User.Password(); ok {
			environment = append(environment, "PGPASSWORD="+password)
		}
	}
	if mode := parsed.Query().Get("sslmode"); mode != "" {
		environment = append(environment, "PGSSLMODE="+mode)
	}
	return environment, nil
}

// classify reduces a failure to a stable code. Underlying command output is
// dropped because it routinely names hosts, roles, and file paths.
func classify(err error) pgtype.Text {
	code := "backup_failed"
	var exitError *exec.ExitError
	switch {
	case errors.Is(err, ErrCorrupt), errors.Is(err, ErrBadFormat):
		code = "verification_failed"
	case errors.As(err, &exitError):
		code = fmt.Sprintf("command_exit_%d", exitError.ExitCode())
	case errors.Is(err, os.ErrNotExist):
		code = "file_missing"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		code = "cancelled"
	}
	return pgtype.Text{String: code, Valid: true}
}

type countingWriter struct{ total int64 }

func (writer *countingWriter) Write(payload []byte) (int, error) {
	writer.total += int64(len(payload))
	return len(payload), nil
}
