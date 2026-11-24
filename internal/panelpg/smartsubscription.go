package panelpg

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/smartsub"
	"github.com/omniflow/omniflow/internal/smartsubpg"
)

// SmartSubscription is the smart subscription's settings with the version
// the panel sends back on save.
type SmartSubscription struct {
	smartsub.Settings
	Version int32 `json:"version"`
}

// SmartSubscription reads the module's settings.
func (service *Service) SmartSubscription(ctx context.Context) (SmartSubscription, error) {
	var setting SmartSubscription
	err := service.inTxRaw(ctx, func(tx pgx.Tx, _ *dbgen.Queries) error {
		var err error
		setting, err = readSmartSubscription(ctx, tx)
		return err
	})
	return setting, err
}

func readSmartSubscription(ctx context.Context, tx pgx.Tx) (SmartSubscription, error) {
	settings, err := smartsubpg.ReadSettings(ctx, tx)
	if err != nil {
		return SmartSubscription{}, err
	}
	var version int32
	err = tx.QueryRow(ctx, `SELECT version FROM smart_subscription_settings WHERE singleton`).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return SmartSubscription{Settings: settings, Version: 0}, nil
	}
	return SmartSubscription{Settings: settings, Version: version}, err
}

// SaveSmartSubscription validates and stores the module's settings. The
// expected version makes two panel tabs saving at once a conflict rather than
// a silent overwrite; a validation failure is a *smartsub.FieldError naming
// the field.
func (service *Service) SaveSmartSubscription(
	ctx context.Context, setting SmartSubscription, actor Actor,
) (SmartSubscription, error) {
	normalized, err := setting.Settings.Normalize()
	if err != nil {
		return SmartSubscription{}, err
	}
	document, err := json.Marshal(normalized)
	if err != nil {
		return SmartSubscription{}, err
	}
	operator, _ := parseUUID(actor.AdminID)
	var saved SmartSubscription
	err = service.inTxRaw(ctx, func(tx pgx.Tx, queries *dbgen.Queries) error {
		var version int32
		if txErr := tx.QueryRow(ctx, `UPDATE smart_subscription_settings
			SET enabled = $1, document = $2, version = version + 1, updated_at = now(), updated_by = $3
			WHERE singleton AND version = $4 RETURNING version`,
			normalized.Enabled, document, operator, setting.Version).Scan(&version); txErr != nil {
			return conflicted(txErr)
		}
		var txErr error
		if saved, txErr = readSmartSubscription(ctx, tx); txErr != nil {
			return txErr
		}
		roles := map[string]int{}
		for _, override := range saved.Hosts {
			if override.Role != "" {
				roles[string(override.Role)]++
			}
		}
		// The audit names what changed in kind, not the domains: a direct list
		// can name the operator's own infrastructure.
		return appendAudit(ctx, queries, actor.audit(
			"panel.smart_subscription.updated", "configuration", "smart_subscription", "singleton",
			map[string]any{
				"enabled": saved.Enabled, "version": saved.Version, "customRules": saved.CustomRules,
				"maxRules": saved.MaxRules, "rules": len(saved.Rules), "outageNotices": saved.OutageNotices,
				"hostOverrides": len(saved.Hosts), "roles": roles,
			},
		))
	})
	return saved, err
}
