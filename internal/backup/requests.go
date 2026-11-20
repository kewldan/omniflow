package backup

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// MaxUpload bounds an uploaded backup file.
const MaxUpload = 2 << 30

// ErrUnreadableUpload refuses a file that does not open with this
// installation's backup key: it is from another installation, corrupted, or
// not a backup at all.
var ErrUnreadableUpload = errors.New("the file does not open with this installation's backup key")

// Delete removes a backup's file and marks it pruned. The row stays, so the
// history of what existed remains auditable.
func (service *Service) Delete(ctx context.Context, backupID string) error {
	record, err := service.get(ctx, backupID)
	if err != nil {
		return err
	}
	if removeErr := os.Remove(service.Path(record)); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return removeErr
	}
	_, err = dbgen.New(service.pool).MarkBackupPruned(ctx, record.ID)
	return err
}

// Import stores an uploaded encrypted backup so it can be restored like any
// other. It is verified before it is recorded: every frame must open with this
// installation's key, which is what stops a file from elsewhere — or a file
// that is not a backup — from ever reaching pg_restore.
func (service *Service) Import(ctx context.Context, source io.Reader, requestedBy string) (dbgen.Backup, error) {
	if !service.Enabled() {
		return dbgen.Backup{}, ErrDisabled
	}
	if err := os.MkdirAll(service.config.Directory, 0o700); err != nil {
		return dbgen.Backup{}, err
	}
	fileName := fmt.Sprintf("omniflow-uploaded-%s.dump.enc", service.clock().UTC().Format("20060102T150405Z"))
	path := filepath.Join(service.config.Directory, fileName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return dbgen.Backup{}, err
	}
	digest := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(file, digest), io.LimitReader(source, MaxUpload+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || size > MaxUpload || size == 0 {
		_ = os.Remove(path)
		return dbgen.Backup{}, errors.Join(ErrUnreadableUpload, copyErr, closeErr)
	}
	sum := digest.Sum(nil)
	if err = service.verifyFile(path, sum); err != nil {
		_ = os.Remove(path)
		return dbgen.Backup{}, ErrUnreadableUpload
	}
	record, err := dbgen.New(service.pool).RecordUploadedBackup(ctx, dbgen.RecordUploadedBackupParams{
		FileName: fileName, RequestedBy: pgtype.Text{String: requestedBy, Valid: requestedBy != ""},
		SizeBytes: size, Sha256: sum,
		RetainUntil: pgtype.Timestamptz{Time: service.clock().Add(service.retentionPeriod()), Valid: true},
	})
	if err != nil {
		_ = os.Remove(path)
	}
	return record, err
}

// ProcessRequest carries out the oldest backup request the panel recorded:
// take a backup now, restore one, or delete one. It returns false when there
// was nothing to do.
func (service *Service) ProcessRequest(ctx context.Context) (bool, error) {
	queries := dbgen.New(service.pool)
	request, err := queries.ClaimBackupRequest(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	operator := uuidText(request.RequestedBy)
	backupID := uuidText(request.BackupID)
	var actionErr error
	switch request.Action {
	case "create":
		_, actionErr = service.Create(ctx, "manual", "panel:"+operator)
	case "restore":
		_, actionErr = service.Restore(ctx, backupID, "panel:"+operator, request.Reason)
	case "delete":
		actionErr = service.Delete(ctx, backupID)
	}
	status, code := "completed", pgtype.Text{}
	if actionErr != nil {
		status, code = "failed", classify(actionErr)
		if !code.Valid {
			code = pgtype.Text{String: "failed", Valid: true}
		}
	}
	if err = queries.FinishBackupRequest(context.WithoutCancel(ctx), dbgen.FinishBackupRequestParams{
		RequestID: request.ID, Status: status, ErrorCode: code,
	}); err != nil {
		return true, err
	}
	return true, actionErr
}
