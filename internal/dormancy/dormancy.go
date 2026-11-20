// Package dormancy marks customers who stopped using the service as dormant
// and, only when an owner asks for it, deletes the ones who never came back.
//
// It is Omniflow's answer to Bedolaga's INACTIVE_USER_DELETE_MONTHS, with one
// deliberate difference. Bedolaga deletes an inactive user outright; here the
// first step is a flag, users.dormant_at, which changes nothing a customer can
// see and is cleared by a database trigger the moment they sign in or write to
// the bot. Deletion is a second, opt-in step that goes through the same
// deletion flow an operator starts from the customer card, so the privacy and
// retention rules apply unchanged.
package dormancy

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/installsettings"
)

// Section is the installation settings section the pass reads.
const Section = "lifecycle"

const (
	defaultInactiveDays = 180
	batchSize           = 500
)

// Settings are the owner's choices, read on every pass.
type Settings struct {
	// Enabled turns the pass on. Off by default: an installation decides for
	// itself whether quiet customers are worth tracking.
	Enabled bool
	// InactiveDays without any activity before a customer is marked dormant.
	InactiveDays int
	// DeleteAfterDays of dormancy before the account is deleted. Zero never
	// deletes anybody.
	DeleteAfterDays int
}

// SettingsFrom reads the lifecycle section.
func SettingsFrom(document installsettings.Document) Settings {
	return Settings{
		Enabled:         document.Bool("dormancyEnabled", false),
		InactiveDays:    document.Positive("inactiveDays", defaultInactiveDays),
		DeleteAfterDays: max(document.Int("deleteAfterDays", 0), 0),
	}
}

// Result is what one pass did.
type Result struct {
	MarkedDormant int
	Deleted       int
}

// Pass runs the dormancy rules.
type Pass struct {
	pool     *pgxpool.Pool
	settings *installsettings.Reader
	logger   *slog.Logger
}

// New builds the pass.
func New(pool *pgxpool.Pool, settings *installsettings.Reader, logger *slog.Logger) *Pass {
	return &Pass{pool: pool, settings: settings, logger: logger}
}

// Run is the worker task: one pass with the current settings.
func (pass *Pass) Run(ctx context.Context) error {
	settings := SettingsFrom(pass.settings.Section(ctx, Section))
	if !settings.Enabled {
		return nil
	}
	result, err := pass.RunOnce(ctx, settings)
	if err != nil {
		return err
	}
	if result.MarkedDormant > 0 || result.Deleted > 0 {
		pass.logger.Info("dormancy pass", "marked_dormant", result.MarkedDormant, "deleted", result.Deleted)
	}
	return nil
}

// RunOnce applies the given settings once, in batches, until nothing is left.
func (pass *Pass) RunOnce(ctx context.Context, settings Settings) (Result, error) {
	var result Result
	for {
		marked, err := pass.markBatch(ctx, settings.InactiveDays)
		if err != nil {
			return result, err
		}
		result.MarkedDormant += marked
		if marked < batchSize {
			break
		}
	}
	if settings.DeleteAfterDays == 0 {
		return result, nil
	}
	for {
		deleted, err := pass.deleteBatch(ctx, settings.DeleteAfterDays)
		if err != nil {
			return result, err
		}
		result.Deleted += deleted
		if deleted < batchSize {
			break
		}
	}
	return result, nil
}

func (pass *Pass) markBatch(ctx context.Context, inactiveDays int) (int, error) {
	var marked int
	err := pgx.BeginFunc(ctx, pass.pool, func(tx pgx.Tx) error {
		queries := dbgen.New(tx)
		ids, err := queries.MarkDormantCustomers(ctx, dbgen.MarkDormantCustomersParams{
			InactiveDays: int32(inactiveDays), BatchSize: batchSize,
		})
		if err != nil {
			return fmt.Errorf("mark dormant: %w", err)
		}
		reason := fmt.Sprintf("no activity for %d days", inactiveDays)
		for _, id := range ids {
			if _, err = queries.InsertCustomerLifecycleEvent(ctx, dbgen.InsertCustomerLifecycleEventParams{
				UserID: id, Action: "dormant", Reason: reason, ActorType: "system",
			}); err != nil {
				return fmt.Errorf("record dormancy: %w", err)
			}
		}
		marked = len(ids)
		return nil
	})
	return marked, err
}

func (pass *Pass) deleteBatch(ctx context.Context, dormantDays int) (int, error) {
	ids, err := dbgen.New(pass.pool).ListDormantForDeletion(ctx, dbgen.ListDormantForDeletionParams{
		DormantDays: int32(dormantDays), BatchSize: batchSize,
	})
	if err != nil {
		return 0, fmt.Errorf("list dormant for deletion: %w", err)
	}
	reason := fmt.Sprintf("dormant for %d days", dormantDays)
	deleted := 0
	for _, id := range ids {
		if err = pass.requestDeletion(ctx, id, reason); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

// requestDeletion starts the same deletion an operator starts from the
// customer card, recorded as the system's. The customer is re-checked inside
// the transaction, so one who came back between the listing and now stays.
func (pass *Pass) requestDeletion(ctx context.Context, id pgtype.UUID, reason string) error {
	return pgx.BeginFunc(ctx, pass.pool, func(tx pgx.Tx) error {
		var still bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
				SELECT 1 FROM users WHERE id = $1 AND status = 'active' AND dormant_at IS NOT NULL
				FOR UPDATE)`, id).Scan(&still); err != nil {
			return err
		}
		if !still {
			return nil
		}
		queries := dbgen.New(tx)
		if _, err := queries.ApplyCustomerLifecycle(ctx, dbgen.ApplyCustomerLifecycleParams{
			UserID: id, Status: "deleted",
		}); err != nil {
			return fmt.Errorf("delete dormant customer: %w", err)
		}
		_, err := queries.InsertCustomerLifecycleEvent(ctx, dbgen.InsertCustomerLifecycleEventParams{
			UserID: id, Action: "deletion_requested", Reason: reason, ActorType: "system",
		})
		return err
	})
}
