package panelpg

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// MaxBulkTargets bounds one bulk action.
//
// It is a limit rather than a page: a change an operator cannot review the
// preview of is a change nobody has actually approved. An operator with more
// targets than this narrows the selection and runs it again, and each run keeps
// its own reason and its own audit event.
const MaxBulkTargets = 1000

// BulkOperation is one previewed-then-applied operator action.
type BulkOperation struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Status      string          `json:"status"`
	RequestedBy string          `json:"requestedBy"`
	Reason      string          `json:"reason"`
	Parameters  json.RawMessage `json:"parameters"`
	Total       int32           `json:"total"`
	Succeeded   int32           `json:"succeeded"`
	Failed      int32           `json:"failed"`
	Skipped     int32           `json:"skipped"`
	CreatedAt   time.Time       `json:"createdAt"`
	CompletedAt *time.Time      `json:"completedAt,omitempty"`
}

// BulkItem is one target and its outcome.
type BulkItem struct {
	Position    int32      `json:"position"`
	TargetType  string     `json:"targetType"`
	TargetID    string     `json:"targetId"`
	Status      string     `json:"status"`
	ErrorCode   string     `json:"errorCode,omitempty"`
	ProcessedAt *time.Time `json:"processedAt,omitempty"`
}

// BulkTarget names one record a bulk action will touch.
type BulkTarget struct {
	Type string
	ID   string
}

// BulkInput describes a bulk action to preview.
type BulkInput struct {
	Kind    string
	Targets []BulkTarget
	// Parameters carry whatever the kind needs — a number of days to extend by,
	// an amount to credit — and are validated by the worker that applies them.
	Parameters json.RawMessage
	// IdempotencyKey makes a resubmitted preview return the operation that
	// already exists rather than creating a second one.
	IdempotencyKey string
}

// PreviewBulkOperation records a bulk action and its targets without applying
// anything.
//
// Nothing runs until StartBulkOperation is called, and that call only accepts
// an operation in the `ready` state. The two-step shape is what makes "impact
// preview before bulk change" a property of the database rather than a habit of
// the panel: there is no path from a request to an applied change that skips
// the preview.
func (service *Service) PreviewBulkOperation(
	ctx context.Context, input BulkInput, actor Actor,
) (BulkOperation, error) {
	if strings.TrimSpace(actor.Reason) == "" || actor.AdminID == "" {
		return BulkOperation{}, ErrValidaton
	}
	if len(input.Targets) == 0 || len(input.Targets) > MaxBulkTargets {
		return BulkOperation{}, ErrValidaton
	}
	if strings.TrimSpace(input.IdempotencyKey) == "" {
		return BulkOperation{}, ErrValidaton
	}
	admin, err := parseUUID(actor.AdminID)
	if err != nil {
		return BulkOperation{}, err
	}
	parameters := input.Parameters
	if len(parameters) == 0 {
		parameters = json.RawMessage("{}")
	}

	var operation BulkOperation
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.CreateBulkOperation(ctx, dbgen.CreateBulkOperationParams{
			Kind: input.Kind, RequestedBy: admin, Reason: actor.Reason,
			Parameters: parameters, IdempotencyKey: input.IdempotencyKey,
		})
		if txErr != nil {
			// A resubmitted preview is the same preview. Reading the existing
			// operation back is what makes a double-submitted form harmless.
			if conflicted(txErr) == ErrConflict {
				existing, readErr := queries.GetBulkOperationByIdempotency(ctx, input.IdempotencyKey)
				if readErr != nil {
					return notFound(readErr)
				}
				operation = bulkOperationFrom(existing)
				return nil
			}
			return txErr
		}

		for position, target := range input.Targets {
			targetID, parseErr := parseUUID(target.ID)
			if parseErr != nil {
				return ErrValidaton
			}
			if txErr := queries.InsertBulkOperationItem(ctx, dbgen.InsertBulkOperationItemParams{
				OperationID: row.ID, Position: int32(position),
				TargetType: target.Type, TargetID: targetID,
			}); txErr != nil {
				return txErr
			}
		}

		ready, txErr := queries.SetBulkOperationTotal(ctx, dbgen.SetBulkOperationTotalParams{
			OperationID: row.ID, TotalCount: int32(len(input.Targets)),
		})
		if txErr != nil {
			return rejected(txErr)
		}
		operation = bulkOperationFrom(ready)

		return appendAudit(ctx, queries, actor.audit(
			"panel.bulk.previewed", "customer", "bulk_operation", operation.ID,
			map[string]any{"kind": input.Kind, "targets": len(input.Targets)},
		))
	})
	return operation, err
}

// StartBulkOperation moves a previewed operation into the running state.
//
// Only a `ready` operation may start, so an operation that is already running,
// finished, or cancelled cannot be started a second time by a resubmitted form.
func (service *Service) StartBulkOperation(
	ctx context.Context, operationID string, actor Actor,
) (BulkOperation, error) {
	id, err := parseUUID(operationID)
	if err != nil {
		return BulkOperation{}, err
	}

	var operation BulkOperation
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.StartBulkOperation(ctx, id)
		if txErr != nil {
			return rejected(txErr)
		}
		operation = bulkOperationFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"panel.bulk.started", "customer", "bulk_operation", operationID,
			map[string]any{"kind": row.Kind, "targets": row.TotalCount},
		))
	})
	return operation, err
}

// CancelBulkOperation abandons an operation that has not started.
//
// A running operation is deliberately not cancellable: some of its targets have
// already been changed, and a half-applied action that reports "cancelled"
// would misrepresent what happened. It runs to completion and the per-item
// results say exactly which targets were touched.
func (service *Service) CancelBulkOperation(
	ctx context.Context, operationID string, actor Actor,
) error {
	id, err := parseUUID(operationID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.CancelBulkOperation(ctx, id); txErr != nil {
			return rejected(txErr)
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.bulk.cancelled", "customer", "bulk_operation", operationID, nil,
		))
	})
}

// RecordBulkItem stores one target's outcome and refreshes the counters.
//
// The counters are recomputed from the items rather than incremented, so a
// retried worker cannot double-count an outcome it already recorded. The
// operation completes automatically once no item is pending.
func (service *Service) RecordBulkItem(
	ctx context.Context, operationID string, position int32, status, errorCode string,
) (BulkOperation, error) {
	id, err := parseUUID(operationID)
	if err != nil {
		return BulkOperation{}, err
	}

	var operation BulkOperation
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.CompleteBulkOperationItem(ctx, dbgen.CompleteBulkOperationItemParams{
			OperationID: id, Position: position, Status: status,
			ErrorCode: optionalText(errorCode),
		}); txErr != nil {
			// An item that is no longer pending was already recorded. That is a
			// replay, not a failure, and the counters below are still correct.
			if rejected(txErr) != ErrRejected {
				return txErr
			}
		}
		row, txErr := queries.RecountBulkOperation(ctx, id)
		if txErr != nil {
			return notFound(txErr)
		}
		operation = bulkOperationFrom(row)
		return nil
	})
	return operation, err
}

// PendingBulkItems returns the next targets a worker should process.
func (service *Service) PendingBulkItems(
	ctx context.Context, operationID string, limit int32,
) ([]BulkItem, error) {
	id, err := parseUUID(operationID)
	if err != nil {
		return nil, err
	}
	rows, err := service.queries().ListPendingBulkOperationItems(
		ctx, dbgen.ListPendingBulkOperationItemsParams{OperationID: id, PageSize: pageSize(limit)},
	)
	if err != nil {
		return nil, err
	}
	return bulkItemsFrom(rows), nil
}

// BulkItems returns every target and its outcome, which is the per-item result
// an operator reviews after a run.
func (service *Service) BulkItems(
	ctx context.Context, operationID string, limit int32,
) ([]BulkItem, error) {
	id, err := parseUUID(operationID)
	if err != nil {
		return nil, err
	}
	rows, err := service.queries().ListBulkOperationItems(
		ctx, dbgen.ListBulkOperationItemsParams{OperationID: id, PageSize: pageSize(limit)},
	)
	if err != nil {
		return nil, err
	}
	return bulkItemsFrom(rows), nil
}

// ListBulkOperations reads the recent bulk actions.
func (service *Service) ListBulkOperations(ctx context.Context, limit int32) ([]BulkOperation, error) {
	rows, err := service.queries().ListBulkOperations(ctx, pageSize(limit))
	if err != nil {
		return nil, err
	}
	operations := make([]BulkOperation, 0, len(rows))
	for _, row := range rows {
		operations = append(operations, bulkOperationFrom(row))
	}
	return operations, nil
}

// BulkOperation reads one operation.
func (service *Service) BulkOperation(ctx context.Context, operationID string) (BulkOperation, error) {
	id, err := parseUUID(operationID)
	if err != nil {
		return BulkOperation{}, err
	}
	row, err := service.queries().GetBulkOperation(ctx, id)
	if err != nil {
		return BulkOperation{}, notFound(err)
	}
	return bulkOperationFrom(row), nil
}

func bulkItemsFrom(rows []dbgen.BulkOperationItem) []BulkItem {
	items := make([]BulkItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, BulkItem{
			Position:    row.Position,
			TargetType:  row.TargetType,
			TargetID:    uuidString(row.TargetID),
			Status:      row.Status,
			ErrorCode:   textValue(row.ErrorCode),
			ProcessedAt: timePointer(row.ProcessedAt),
		})
	}
	return items
}

func bulkOperationFrom(row dbgen.BulkOperation) BulkOperation {
	return BulkOperation{
		ID:          uuidString(row.ID),
		Kind:        row.Kind,
		Status:      row.Status,
		RequestedBy: uuidString(row.RequestedBy),
		Reason:      row.Reason,
		Parameters:  row.Parameters,
		Total:       row.TotalCount,
		Succeeded:   row.SucceededCount,
		Failed:      row.FailedCount,
		Skipped:     row.SkippedCount,
		CreatedAt:   timeValue(row.CreatedAt),
		CompletedAt: timePointer(row.CompletedAt),
	}
}
