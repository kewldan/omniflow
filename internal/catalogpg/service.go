package catalogpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/commerce"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

type Service struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

type Localization struct{ Name, Description string }
type Price struct {
	Currency    string
	AmountMinor int64
}
type VersionInput struct {
	BillingPeriod                                      string
	DurationSeconds                                    int64
	TrafficAllowanceBytes                              *int64
	DeviceLimit                                        *int32
	SquadIDs                                           []string
	UpgradePolicy, DowngradePolicy, CancellationPolicy string
	RecurringCapable                                   bool
	Prices                                             []Price
	// GracePeriodSeconds keeps access alive after expiry so the customer can be
	// told exactly how long they have left to renew. Zero disables the grace
	// period.
	GracePeriodSeconds int64
	// TrialEligibility constrains who may activate a trial plan version:
	// new_customer, never_trialled, or any.
	TrialEligibility string
}
type PlanInput struct {
	Code, Kind    string
	Visible       bool
	SortOrder     int32
	Localizations map[string]Localization
	Version       VersionInput
}

func (service *Service) CreatePlan(ctx context.Context, input PlanInput) (dbgen.Plan, dbgen.PlanVersion, error) {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return dbgen.Plan{}, dbgen.PlanVersion{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbgen.New(tx)
	plan, err := q.CreatePlan(ctx, dbgen.CreatePlanParams{Code: input.Code, Kind: input.Kind, Visible: input.Visible, SortOrder: input.SortOrder})
	if err != nil {
		return dbgen.Plan{}, dbgen.PlanVersion{}, err
	}
	for locale, copy := range input.Localizations {
		if locale != "ru" && locale != "en" {
			return dbgen.Plan{}, dbgen.PlanVersion{}, errors.New("unsupported locale")
		}
		if _, err = q.UpsertPlanLocalization(ctx, dbgen.UpsertPlanLocalizationParams{PlanID: plan.ID, Locale: locale, Name: copy.Name, Description: copy.Description}); err != nil {
			return dbgen.Plan{}, dbgen.PlanVersion{}, err
		}
	}
	version, err := createVersion(ctx, tx, q, plan.ID, input.Version)
	if err != nil {
		return dbgen.Plan{}, dbgen.PlanVersion{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return dbgen.Plan{}, dbgen.PlanVersion{}, err
	}
	return plan, version, nil
}

func (service *Service) CreateVersion(ctx context.Context, planID string, input VersionInput) (dbgen.PlanVersion, error) {
	id, err := parseUUID(planID)
	if err != nil {
		return dbgen.PlanVersion{}, err
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return dbgen.PlanVersion{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	version, err := createVersion(ctx, tx, dbgen.New(tx), id, input)
	if err != nil {
		return dbgen.PlanVersion{}, err
	}
	return version, tx.Commit(ctx)
}

func createVersion(ctx context.Context, tx pgx.Tx, q *dbgen.Queries, planID pgtype.UUID, input VersionInput) (dbgen.PlanVersion, error) {
	if input.DurationSeconds <= 0 || len(input.Prices) == 0 {
		return dbgen.PlanVersion{}, errors.New("duration and prices are required")
	}
	if input.GracePeriodSeconds < 0 {
		return dbgen.PlanVersion{}, errors.New("grace period cannot be negative")
	}
	if input.TrialEligibility == "" {
		input.TrialEligibility = "new_customer"
	}
	switch input.TrialEligibility {
	case "new_customer", "never_trialled", "any":
	default:
		return dbgen.PlanVersion{}, errors.New("unsupported trial eligibility rule")
	}
	next, err := q.NextPlanVersion(ctx, planID)
	if err != nil {
		return dbgen.PlanVersion{}, err
	}
	squadIDs, err := databaseutil.ParseUUIDs(input.SquadIDs)
	if err != nil {
		return dbgen.PlanVersion{}, err
	}
	version, err := q.CreatePlanVersion(ctx, dbgen.CreatePlanVersionParams{PlanID: planID, Version: next, BillingPeriod: input.BillingPeriod, DurationSeconds: input.DurationSeconds, TrafficAllowanceBytes: optionalInt8(input.TrafficAllowanceBytes), DeviceLimit: optionalInt4(input.DeviceLimit), RemnawaveSquadIds: squadIDs, UpgradePolicy: input.UpgradePolicy, DowngradePolicy: input.DowngradePolicy, CancellationPolicy: input.CancellationPolicy, RecurringCapable: input.RecurringCapable})
	if err != nil {
		return dbgen.PlanVersion{}, err
	}
	// The lifecycle fields live outside the generated insert so the v0.3 query
	// stays untouched; they are set in the same transaction, so a version is
	// never visible without its grace period and trial rule.
	if _, err = tx.Exec(ctx, `UPDATE plan_versions SET grace_period_seconds = $2, trial_eligibility = $3
		WHERE id = $1`, version.ID, input.GracePeriodSeconds, input.TrialEligibility); err != nil {
		return dbgen.PlanVersion{}, err
	}
	for _, price := range input.Prices {
		if _, err := commerce.NewMoney(price.AmountMinor, price.Currency); err != nil {
			return dbgen.PlanVersion{}, err
		}
		if _, err = q.CreatePlanPrice(ctx, dbgen.CreatePlanPriceParams{PlanVersionID: version.ID, Currency: price.Currency, AmountMinor: price.AmountMinor}); err != nil {
			return dbgen.PlanVersion{}, err
		}
	}
	return version, nil
}

type PromotionInput struct {
	Code, Kind, Currency string
	Value                int64
	StartsAt, EndsAt     *time.Time
	RedemptionLimit      *int32
	PerCustomerLimit     int32
	Eligibility          map[string]any
	Active               bool
	PlanIDs              []string
	Codes                []string
}

func (service *Service) CreatePromotion(ctx context.Context, input PromotionInput) (dbgen.Promotion, error) {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return dbgen.Promotion{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbgen.New(tx)
	eligibility, _ := json.Marshal(input.Eligibility)
	promotion, err := q.CreatePromotion(ctx, dbgen.CreatePromotionParams{Code: input.Code, Kind: input.Kind, Value: input.Value, Currency: optionalText(input.Currency), StartsAt: optionalTime(input.StartsAt), EndsAt: optionalTime(input.EndsAt), RedemptionLimit: optionalInt4(input.RedemptionLimit), PerCustomerLimit: input.PerCustomerLimit, Eligibility: eligibility, Active: input.Active})
	if err != nil {
		return dbgen.Promotion{}, err
	}
	for _, rawID := range input.PlanIDs {
		id, parseErr := parseUUID(rawID)
		if parseErr != nil {
			return dbgen.Promotion{}, parseErr
		}
		if err = q.AddPromotionPlan(ctx, dbgen.AddPromotionPlanParams{PromotionID: promotion.ID, PlanID: id}); err != nil {
			return dbgen.Promotion{}, err
		}
	}
	for _, rawCode := range input.Codes {
		code, normalizeErr := commerce.NormalizePromoCode(rawCode)
		if normalizeErr != nil {
			return dbgen.Promotion{}, normalizeErr
		}
		if _, err = q.CreatePromoCode(ctx, dbgen.CreatePromoCodeParams{PromotionID: promotion.ID, NormalizedCode: code, Active: true}); err != nil {
			return dbgen.Promotion{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return dbgen.Promotion{}, err
	}
	return promotion, nil
}

func parseUUID(value string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil || !id.Valid {
		return pgtype.UUID{}, fmt.Errorf("invalid UUID")
	}
	return id, nil
}
func optionalText(v string) pgtype.Text { return pgtype.Text{String: v, Valid: v != ""} }
func optionalTime(v *time.Time) pgtype.Timestamptz {
	if v == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *v, Valid: true}
}
func optionalInt8(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}
func optionalInt4(v *int32) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *v, Valid: true}
}
