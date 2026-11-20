package panelpg

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// BackupRequestInput is what the panel asks the worker to do.
type BackupRequestInput struct {
	// Action is "create", "restore", or "delete".
	Action   string `json:"action"`
	BackupID string `json:"backupId"`
	Reason   string `json:"reason"`
}

// BackupRequest is one request and how it ended.
type BackupRequest struct {
	ID         string     `json:"id"`
	Action     string     `json:"action"`
	BackupID   string     `json:"backupId,omitempty"`
	Status     string     `json:"status"`
	ErrorCode  string     `json:"errorCode,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// RequestBackupAction records a backup, restore, or deletion for the worker.
// A restore replays the backup over the live database, so it needs a reason,
// and every request is audited before the worker acts on it.
func (service *Service) RequestBackupAction(ctx context.Context, input BackupRequestInput, actor Actor) (BackupRequest, error) {
	reason := strings.TrimSpace(input.Reason)
	backupID := pgtype.UUID{}
	switch input.Action {
	case "create":
	case "restore", "delete":
		parsed, err := parseUUID(input.BackupID)
		if err != nil {
			return BackupRequest{}, err
		}
		backupID = parsed
		if input.Action == "restore" && reason == "" {
			return BackupRequest{}, ErrValidaton
		}
	default:
		return BackupRequest{}, ErrValidaton
	}
	requestedBy := pgtype.UUID{}
	if actor.AdminID != "" {
		requestedBy, _ = parseUUID(actor.AdminID)
	}
	var created BackupRequest
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, err := queries.CreateBackupRequest(ctx, dbgen.CreateBackupRequestParams{
			Action: input.Action, BackupID: backupID, Reason: reason, RequestedBy: requestedBy,
		})
		if err != nil {
			return notFound(err)
		}
		created = backupRequestFrom(row)
		return appendAudit(ctx, queries, actor.audit("system.backup."+input.Action+"_requested", "system",
			"backup", orAll(input.BackupID), map[string]any{"requestId": created.ID, "reason": reason}))
	})
	return created, err
}

// BackupRequests lists the latest requests.
func (service *Service) BackupRequests(ctx context.Context) ([]BackupRequest, error) {
	rows, err := service.queries().ListBackupRequests(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]BackupRequest, 0, len(rows))
	for _, row := range rows {
		out = append(out, backupRequestFrom(row))
	}
	return out, nil
}

// RecordAction audits an operator action another service performed.
func (service *Service) RecordAction(
	ctx context.Context, action, category, targetType, targetID string, metadata map[string]any, actor Actor,
) error {
	return appendAudit(ctx, service.queries(), actor.audit(action, category, targetType, targetID, metadata))
}

func backupRequestFrom(row dbgen.BackupRequest) BackupRequest {
	request := BackupRequest{
		ID: uuidString(row.ID), Action: row.Action, Status: row.Status, ErrorCode: row.ErrorCode.String,
		CreatedAt: row.CreatedAt.Time, FinishedAt: timePointer(row.FinishedAt),
	}
	if row.BackupID.Valid {
		request.BackupID = uuidString(row.BackupID)
	}
	return request
}
