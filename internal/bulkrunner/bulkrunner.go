// Package bulkrunner applies the bulk actions an operator previewed.
//
// The two-step shape it serves — preview, then start — is what makes "impact
// preview before a bulk change" a property of the database rather than a habit
// of the panel: nothing here can run an operation that is not already in the
// `running` state with every target recorded, so there is no path from a
// request to an applied change that skips the preview.
//
// Every item is applied one at a time and recorded one at a time. That is
// slower than a set-based update and it is the point: a bulk action that half
// succeeds must leave a per-item record of which half, because "extend 400
// subscriptions" that reports only "failed" is not something an operator can
// act on.
package bulkrunner

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/fulfillment"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// batchSize bounds one pass over a single operation.
const batchSize = 50

// Config tunes the loop. The zero value is the documented default.
type Config struct {
	Interval time.Duration
}

// Runner applies queued bulk operations.
type Runner struct {
	pool        *pgxpool.Pool
	operations  *panelpg.Service
	fulfillment *fulfillment.Service
	// wallet applies a bulk credit through the ordinary double-entry
	// adjustment, so a balance is always explicable from the ledger.
	wallet *commercepg.Store
	logger *slog.Logger
	config Config
}

// New builds the bulk runner. A nil operations service disables it, which is
// what a deployment without the panel's encryption key gets.
func New(
	pool *pgxpool.Pool, operations *panelpg.Service,
	fulfillmentService *fulfillment.Service, wallet *commercepg.Store,
	logger *slog.Logger, config Config,
) *Runner {
	if config.Interval <= 0 {
		config.Interval = 15 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{
		pool: pool, operations: operations, fulfillment: fulfillmentService,
		wallet: wallet, logger: logger, config: config,
	}
}

// Run applies queued operations until the context is cancelled.
func (runner *Runner) Run(ctx context.Context) {
	if runner.operations == nil {
		runner.logger.Info("bulk runner disabled", "reason", "operations service is not configured")
		return
	}
	ticker := time.NewTicker(runner.config.Interval)
	defer ticker.Stop()
	for {
		runner.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RunOnce applies one batch from every running operation.
func (runner *Runner) RunOnce(ctx context.Context) {
	operations, err := runner.operations.ListBulkOperations(ctx, 20)
	if err != nil {
		runner.logger.Error("bulk operation lookup failed", "error", err)
		return
	}
	for _, operation := range operations {
		if ctx.Err() != nil {
			return
		}
		if operation.Status != "running" {
			continue
		}
		runner.apply(ctx, operation)
	}
}

// apply processes one batch of an operation's pending items.
func (runner *Runner) apply(ctx context.Context, operation panelpg.BulkOperation) {
	items, err := runner.operations.PendingBulkItems(ctx, operation.ID, batchSize)
	if err != nil {
		runner.logger.Error("bulk item lookup failed", "operationId", operation.ID, "error", err)
		return
	}
	for _, item := range items {
		if ctx.Err() != nil {
			return
		}
		status, errorCode := runner.applyItem(ctx, operation, item)
		if _, err := runner.operations.RecordBulkItem(
			ctx, operation.ID, item.Position, status, errorCode,
		); err != nil {
			// The change may already have been made, so the honest thing is to
			// log loudly rather than retry blindly: a retried extend would add
			// a second period.
			runner.logger.Error("bulk item bookkeeping failed",
				"operationId", operation.ID, "position", item.Position, "error", err)
		}
	}
}

// applyItem performs one target's change.
//
// The idempotency key names the operation and the position, so an item that is
// retried after a crash reaches the same fulfillment operation rather than
// applying a second time. That is what makes the runner safe to restart in the
// middle of a batch.
func (runner *Runner) applyItem(
	ctx context.Context, operation panelpg.BulkOperation, item panelpg.BulkItem,
) (status, errorCode string) {
	if item.TargetType != "subscription" && operation.Kind != "wallet_credit" {
		return "skipped", "unsupported_target"
	}
	switch operation.Kind {
	case "subscription_extend":
		return runner.extend(ctx, operation, item)
	case "subscription_enable":
		return runner.lifecycle(ctx, operation, item, "enable")
	case "subscription_disable":
		return runner.lifecycle(ctx, operation, item, "disable")
	case "wallet_credit":
		return runner.credit(ctx, operation, item)
	case "customer_export":
		// The export is produced by the read path, not by mutating anything.
		// Marking the item succeeded records that it was included.
		return "succeeded", ""
	default:
		return "skipped", "unsupported_kind"
	}
}

// creditParameters is what a wallet_credit operation carries.
type creditParameters struct {
	AmountMinor int64  `json:"amountMinor"`
	Currency    string `json:"currency"`
}

// credit adds wallet balance to one customer.
//
// It goes through the ordinary wallet adjustment, which writes a double-entry
// ledger transaction keyed on this item. A replayed item therefore credits
// nothing a second time, and the resulting balance is explicable from the
// ledger rather than from a counter somebody incremented.
func (runner *Runner) credit(
	ctx context.Context, operation panelpg.BulkOperation, item panelpg.BulkItem,
) (string, string) {
	var parameters creditParameters
	if err := json.Unmarshal(operation.Parameters, &parameters); err != nil ||
		parameters.AmountMinor <= 0 || parameters.Currency == "" {
		return "failed", "invalid_parameters"
	}
	if runner.wallet == nil {
		return "failed", "wallet_unavailable"
	}
	key := "bulk:" + operation.ID + ":" + itemKey(item.Position)
	if err := runner.wallet.AdjustWallet(
		ctx, item.TargetID, parameters.Currency, parameters.AmountMinor,
		"credit", "bulk_operation:"+operation.ID, key, operation.Reason, operation.RequestedBy,
	); err != nil {
		runner.logger.Warn("bulk wallet credit failed",
			"operationId", operation.ID, "position", item.Position, "error", err)
		return "failed", "credit_failed"
	}
	return "succeeded", ""
}

// extendParameters is what a subscription_extend operation carries.
type extendParameters struct {
	Days int `json:"days"`
}

func (runner *Runner) extend(
	ctx context.Context, operation panelpg.BulkOperation, item panelpg.BulkItem,
) (string, string) {
	var parameters extendParameters
	if err := json.Unmarshal(operation.Parameters, &parameters); err != nil || parameters.Days <= 0 {
		return "failed", "invalid_parameters"
	}
	target, err := runner.operations.BulkSubscriptionTarget(ctx, item.TargetID)
	if err != nil {
		return "failed", "subscription_unavailable"
	}
	endsAt := target.EndsAt.Add(time.Duration(parameters.Days) * 24 * time.Hour)
	if err := runner.enqueue(ctx, operation, item, target, "extend", &endsAt); err != nil {
		return "failed", failureCode(err)
	}
	return "succeeded", ""
}

func (runner *Runner) lifecycle(
	ctx context.Context, operation panelpg.BulkOperation, item panelpg.BulkItem, action string,
) (string, string) {
	target, err := runner.operations.BulkSubscriptionTarget(ctx, item.TargetID)
	if err != nil {
		return "failed", "subscription_unavailable"
	}
	if err := runner.enqueue(ctx, operation, item, target, action, nil); err != nil {
		return "failed", failureCode(err)
	}
	return "succeeded", ""
}

// enqueue pushes the change through the ordinary fulfillment pipeline.
//
// A bulk change is not a special kind of change. It carries the same
// idempotency key discipline, the same retry policy, the same drift detection,
// and the same history as one an operator made by hand, because a customer
// whose subscription was extended in a batch of four hundred deserves the same
// record as one whose subscription was extended on its own.
func (runner *Runner) enqueue(
	ctx context.Context, operation panelpg.BulkOperation, item panelpg.BulkItem,
	target panelpg.BulkSubscriptionTarget, action string, endsAt *time.Time,
) error {
	if runner.fulfillment == nil {
		return errors.New("fulfillment is not configured")
	}
	key := "bulk:" + operation.ID + ":" + itemKey(item.Position)
	_, err := runner.fulfillment.Enqueue(ctx, target.EntitlementID, fulfillment.OperationInput{
		Operation:      action,
		IdempotencyKey: key,
		CorrelationID:  key,
		EndsAt:         endsAt,
	})
	return err
}

func itemKey(position int32) string { return strconv.FormatInt(int64(position), 10) }

// failureCode reduces an enqueue failure to a stable label an operator can act
// on. The detail is already in the log; the item row carries the class.
func failureCode(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "enqueue_failed"
	}
}
