package panelpg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// SimpleSubscription is the "simple subscription" setting (Bedolaga's
// SIMPLE_SUBSCRIPTION_*): when Enabled, the bot and the web store show the
// one plan PlanID names with one Buy button, the period, traffic, and extra
// devices chosen in advance. Zero and empty values are the version's own.
type SimpleSubscription struct {
	Enabled        bool   `json:"enabled"`
	PlanID         string `json:"planId"`
	PeriodDays     int32  `json:"periodDays"`
	TrafficGB      int32  `json:"trafficGb"`
	DeviceAddonID  string `json:"deviceAddonId"`
	DeviceQuantity int32  `json:"deviceQuantity"`
}

// SimpleSubscription reads the setting.
func (service *Service) SimpleSubscription(ctx context.Context) (SimpleSubscription, error) {
	var setting SimpleSubscription
	err := service.inTxRaw(ctx, func(tx pgx.Tx, _ *dbgen.Queries) error {
		var err error
		setting, err = readSimpleSubscription(ctx, tx)
		return err
	})
	return setting, err
}

func readSimpleSubscription(ctx context.Context, tx pgx.Tx) (SimpleSubscription, error) {
	var (
		setting       SimpleSubscription
		period, gb    pgtype.Int4
		plan, devices pgtype.Text
	)
	err := tx.QueryRow(ctx, `SELECT enabled, plan_id::text, period_days, traffic_gb, device_addon_id::text, device_quantity
		FROM simple_subscription WHERE singleton`).Scan(&setting.Enabled, &plan, &period, &gb, &devices, &setting.DeviceQuantity)
	if errors.Is(err, pgx.ErrNoRows) {
		return SimpleSubscription{}, nil
	}
	setting.PlanID, setting.PeriodDays, setting.TrafficGB, setting.DeviceAddonID = plan.String, period.Int32, gb.Int32, devices.String
	return setting, err
}

// SaveSimpleSubscription replaces the setting. An enabled one needs a plan
// with a live version; extra devices need a devices add-on. Whether the
// version sells the chosen period and traffic is checked by pricing them at
// the Buy button, the same rule every checkout applies.
func (service *Service) SaveSimpleSubscription(ctx context.Context, setting SimpleSubscription, actor Actor) (SimpleSubscription, error) {
	if setting.PeriodDays < 0 || setting.PeriodDays > 3650 || setting.TrafficGB < 0 || setting.TrafficGB > maxCustomTrafficGB ||
		setting.DeviceQuantity < 0 || setting.DeviceQuantity > 100 || setting.Enabled && setting.PlanID == "" ||
		setting.DeviceQuantity > 0 && setting.DeviceAddonID == "" {
		return SimpleSubscription{}, ErrValidaton
	}
	plan, devices := pgtype.UUID{}, pgtype.UUID{}
	var err error
	if setting.PlanID != "" {
		if plan, err = parseUUID(setting.PlanID); err != nil {
			return SimpleSubscription{}, ErrValidaton
		}
	}
	if setting.DeviceAddonID != "" {
		if devices, err = parseUUID(setting.DeviceAddonID); err != nil {
			return SimpleSubscription{}, ErrValidaton
		}
	}
	// An operator signed in to the panel is recorded; a bearer token is not
	// a person and leaves it empty.
	operator, _ := parseUUID(actor.AdminID)
	var saved SimpleSubscription
	err = service.inTxRaw(ctx, func(tx pgx.Tx, queries *dbgen.Queries) error {
		if plan.Valid {
			var live bool
			if txErr := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM plans p JOIN plan_versions v ON v.plan_id = p.id
				WHERE p.id = $1 AND p.archived_at IS NULL AND v.retired_at IS NULL)`, plan).Scan(&live); txErr != nil {
				return txErr
			}
			if !live {
				return ErrValidaton
			}
		}
		if devices.Valid {
			var kind string
			if txErr := tx.QueryRow(ctx, `SELECT kind FROM addons WHERE id = $1 AND archived_at IS NULL`, devices).Scan(&kind); txErr != nil || kind != "devices" {
				return ErrValidaton
			}
		}
		if _, txErr := tx.Exec(ctx, `UPDATE simple_subscription
			SET enabled = $1, plan_id = $2, period_days = NULLIF($3::integer, 0), traffic_gb = NULLIF($4::integer, 0),
			    device_addon_id = $5, device_quantity = $6, updated_at = now(), updated_by = $7
			WHERE singleton`, setting.Enabled, plan, setting.PeriodDays, setting.TrafficGB, devices, setting.DeviceQuantity,
			operator); txErr != nil {
			return txErr
		}
		var txErr error
		if saved, txErr = readSimpleSubscription(ctx, tx); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.simple_subscription.updated", "configuration", "simple_subscription", "singleton",
			map[string]any{
				"enabled": saved.Enabled, "planId": saved.PlanID, "periodDays": saved.PeriodDays,
				"trafficGb": saved.TrafficGB, "deviceQuantity": saved.DeviceQuantity,
			},
		))
	})
	return saved, err
}
