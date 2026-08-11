package panelpg

import (
	"context"
	"encoding/json"
	"time"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// PlanSummary is one plan as an operator sees it.
//
// `OrderLineCount` is included because archiving a plan that historical orders
// reference is a normal and safe thing to do — plan versions are immutable, so
// those orders keep their own snapshot — but it is a decision an operator
// should make while looking at how many there are.
type PlanSummary struct {
	ID                       string     `json:"id"`
	Code                     string     `json:"code"`
	Kind                     string     `json:"kind"`
	Visible                  bool       `json:"visible"`
	SortOrder                int32      `json:"sortOrder"`
	MaxConcurrentPerCustomer *int32     `json:"maxConcurrentPerCustomer,omitempty"`
	LatestVersion            int32      `json:"latestVersion"`
	OrderLineCount           int64      `json:"orderLineCount"`
	CreatedAt                time.Time  `json:"createdAt"`
	ArchivedAt               *time.Time `json:"archivedAt,omitempty"`
}

// ListPlans reads the catalogue, archived plans included.
func (service *Service) ListPlans(ctx context.Context) ([]PlanSummary, error) {
	rows, err := service.queries().ListPlansAdmin(ctx)
	if err != nil {
		return nil, err
	}
	plans := make([]PlanSummary, 0, len(rows))
	for _, row := range rows {
		plans = append(plans, PlanSummary{
			ID:                       uuidString(row.Plan.ID),
			Code:                     row.Plan.Code,
			Kind:                     row.Plan.Kind,
			Visible:                  row.Plan.Visible,
			SortOrder:                row.Plan.SortOrder,
			MaxConcurrentPerCustomer: int4Pointer(row.Plan.MaxConcurrentPerCustomer),
			LatestVersion:            row.LatestVersion,
			OrderLineCount:           row.OrderLineCount,
			CreatedAt:                timeValue(row.Plan.CreatedAt),
			ArchivedAt:               timePointer(row.Plan.ArchivedAt),
		})
	}
	return plans, nil
}

// PlanVersion is one immutable priced configuration of a plan.
type PlanVersion struct {
	ID                  string           `json:"id"`
	Version             int32            `json:"version"`
	BillingPeriod       string           `json:"billingPeriod"`
	DurationSeconds     int64            `json:"durationSeconds"`
	TrafficAllowance    *int64           `json:"trafficAllowanceBytes,omitempty"`
	DeviceLimit         *int32           `json:"deviceLimit,omitempty"`
	SquadIDs            []string         `json:"squadIds"`
	SquadSelection      string           `json:"squadSelection"`
	MinSelectableSquads int32            `json:"minSelectableSquads"`
	MaxSelectableSquads *int32           `json:"maxSelectableSquads,omitempty"`
	UpgradePolicy       string           `json:"upgradePolicy"`
	DowngradePolicy     string           `json:"downgradePolicy"`
	CancellationPolicy  string           `json:"cancellationPolicy"`
	GracePeriodSeconds  int64            `json:"gracePeriodSeconds"`
	TrialEligibility    string           `json:"trialEligibility"`
	RecurringCapable    bool             `json:"recurringCapable"`
	Prices              map[string]int64 `json:"prices"`
	CreatedAt           time.Time        `json:"createdAt"`
	RetiredAt           *time.Time       `json:"retiredAt,omitempty"`
}

// PlanDetail is one plan with its versions and localisations.
type PlanDetail struct {
	Plan          PlanSummary             `json:"plan"`
	Localizations map[string]Localization `json:"localizations"`
	Versions      []PlanVersion           `json:"versions"`
}

// Localization is a name and description for one locale.
type Localization struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// PlanDetail assembles a plan for the editor.
func (service *Service) PlanDetail(ctx context.Context, planID string) (PlanDetail, error) {
	id, err := parseUUID(planID)
	if err != nil {
		return PlanDetail{}, err
	}
	queries := service.queries()

	plan, err := queries.GetPlanAdmin(ctx, id)
	if err != nil {
		return PlanDetail{}, notFound(err)
	}
	localizations, err := queries.ListPlanLocalizations(ctx, id)
	if err != nil {
		return PlanDetail{}, err
	}
	versions, err := queries.ListPlanVersions(ctx, id)
	if err != nil {
		return PlanDetail{}, err
	}

	detail := PlanDetail{
		Plan: PlanSummary{
			ID:                       uuidString(plan.ID),
			Code:                     plan.Code,
			Kind:                     plan.Kind,
			Visible:                  plan.Visible,
			SortOrder:                plan.SortOrder,
			MaxConcurrentPerCustomer: int4Pointer(plan.MaxConcurrentPerCustomer),
			CreatedAt:                timeValue(plan.CreatedAt),
			ArchivedAt:               timePointer(plan.ArchivedAt),
		},
		Localizations: make(map[string]Localization, len(localizations)),
		Versions:      make([]PlanVersion, 0, len(versions)),
	}
	for _, localization := range localizations {
		detail.Localizations[localization.Locale] = Localization{
			Name: localization.Name, Description: localization.Description,
		}
	}
	for _, version := range versions {
		prices, priceErr := queries.ListPlanPrices(ctx, version.ID)
		if priceErr != nil {
			return PlanDetail{}, priceErr
		}
		converted := PlanVersion{
			ID:                  uuidString(version.ID),
			Version:             version.Version,
			BillingPeriod:       version.BillingPeriod,
			DurationSeconds:     version.DurationSeconds,
			TrafficAllowance:    int8Pointer(version.TrafficAllowanceBytes),
			DeviceLimit:         int4Pointer(version.DeviceLimit),
			SquadIDs:            uuidStrings(version.RemnawaveSquadIds),
			SquadSelection:      version.SquadSelection,
			MinSelectableSquads: version.MinSelectableSquads,
			MaxSelectableSquads: int4Pointer(version.MaxSelectableSquads),
			UpgradePolicy:       version.UpgradePolicy,
			DowngradePolicy:     version.DowngradePolicy,
			CancellationPolicy:  version.CancellationPolicy,
			GracePeriodSeconds:  version.GracePeriodSeconds,
			TrialEligibility:    version.TrialEligibility,
			RecurringCapable:    version.RecurringCapable,
			Prices:              make(map[string]int64, len(prices)),
			CreatedAt:           timeValue(version.CreatedAt),
			RetiredAt:           timePointer(version.RetiredAt),
		}
		for _, price := range prices {
			converted.Prices[price.Currency] = price.AmountMinor
		}
		detail.Versions = append(detail.Versions, converted)
	}
	return detail, nil
}

// PlanPresentation is everything about a plan the panel edits directly.
//
// Visibility, ordering, and the concurrency cap are saved together in one
// statement rather than through three endpoints, so two operators editing the
// same plan cannot each read one field, write it back, and silently drop the
// other's change.
type PlanPresentation struct {
	Visible   bool
	SortOrder int32
	// MaxConcurrent narrows the installation-wide subscription ceiling for this
	// plan and can never widen it. Both are checked at purchase time, so a plan
	// configured above the installation limit is bounded by it rather than
	// overriding it.
	MaxConcurrent *int32
}

// UpdatePlanPresentation saves visibility, ordering, and the concurrency cap.
func (service *Service) UpdatePlanPresentation(
	ctx context.Context, planID string, presentation PlanPresentation, actor Actor,
) error {
	if presentation.MaxConcurrent != nil && *presentation.MaxConcurrent < 1 {
		return ErrValidaton
	}
	id, err := parseUUID(planID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.SetPlanVisibility(ctx, dbgen.SetPlanVisibilityParams{
			ID: id, Visible: presentation.Visible, SortOrder: presentation.SortOrder,
		}); txErr != nil {
			return notFound(txErr)
		}
		if _, txErr := queries.UpdatePlanOrdering(ctx, dbgen.UpdatePlanOrderingParams{
			PlanID:                   id,
			SortOrder:                presentation.SortOrder,
			MaxConcurrentPerCustomer: optionalInt4(presentation.MaxConcurrent),
		}); txErr != nil {
			return notFound(txErr)
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.plan.updated", "configuration", "plan", planID,
			map[string]any{
				"visible": presentation.Visible, "sortOrder": presentation.SortOrder,
				"maxConcurrentPerCustomer": presentation.MaxConcurrent,
			},
		))
	})
}

// ArchivePlan retires a plan from the catalogue, or restores it.
//
// Archiving hides the plan and stops new purchases. It deliberately does not
// touch existing entitlements: a customer who bought a plan keeps what they
// bought, and their order keeps the immutable version snapshot it was priced
// against.
func (service *Service) ArchivePlan(
	ctx context.Context, planID string, archived bool, actor Actor,
) error {
	id, err := parseUUID(planID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.ArchivePlan(ctx, dbgen.ArchivePlanParams{
			PlanID: id, Archived: archived,
		}); txErr != nil {
			return notFound(txErr)
		}
		action := "panel.plan.archived"
		if !archived {
			action = "panel.plan.restored"
		}
		return appendAudit(ctx, queries, actor.audit(
			action, "configuration", "plan", planID, nil,
		))
	})
}

// SavePlanLocalization stores a plan's name and description for one locale.
func (service *Service) SavePlanLocalization(
	ctx context.Context, planID, locale, name, description string, actor Actor,
) error {
	if locale != "ru" && locale != "en" {
		return ErrValidaton
	}
	id, err := parseUUID(planID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.UpsertPlanLocalization(ctx, dbgen.UpsertPlanLocalizationParams{
			PlanID: id, Locale: locale, Name: name, Description: description,
		}); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.plan.localized", "configuration", "plan", planID,
			map[string]any{"locale": locale},
		))
	})
}

// RetirePlanVersion stops a version being sold without archiving the plan.
//
// A retired version is still referenced by every order that bought it, and
// those orders keep their price. That is the whole point of versioning the
// catalogue: retiring is a forward-looking decision and never a rewrite.
func (service *Service) RetirePlanVersion(
	ctx context.Context, planVersionID string, actor Actor,
) error {
	id, err := parseUUID(planVersionID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.RetirePlanVersion(ctx, id); txErr != nil {
			return rejected(txErr)
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.plan_version.retired", "configuration", "plan_version", planVersionID, nil,
		))
	})
}

// ---------------------------------------------------------------------------
// Promotions
// ---------------------------------------------------------------------------

// Promotion is one promotion with its usage.
//
// The reward kinds are `percent` and `fixed`, which discount an order, plus
// `wallet_credit`, `days`, and `trial`, which do not. Grouping them under one
// record is what lets eligibility, redemption limits, stacking, and the audit
// trail be written once rather than five times.
type Promotion struct {
	ID              string          `json:"id"`
	Code            string          `json:"code"`
	Kind            string          `json:"kind"`
	Value           int64           `json:"value"`
	Currency        string          `json:"currency,omitempty"`
	StartsAt        *time.Time      `json:"startsAt,omitempty"`
	EndsAt          *time.Time      `json:"endsAt,omitempty"`
	RedemptionLimit *int32          `json:"redemptionLimit,omitempty"`
	PerCustomer     int32           `json:"perCustomerLimit"`
	Eligibility     json.RawMessage `json:"eligibility"`
	Active          bool            `json:"active"`
	// Stackable is off by default: two promotions combine only when both say
	// they may.
	Stackable bool `json:"stackable"`
	// Precedence orders evaluation, highest first, so applying several is
	// deterministic rather than dependent on the order they were entered.
	Precedence      int32     `json:"precedence"`
	RedemptionCount int64     `json:"redemptionCount"`
	DiscountMinor   int64     `json:"discountMinor"`
	CreatedAt       time.Time `json:"createdAt"`
}

// PromotionFilter is what the promotion list accepts.
type PromotionFilter struct {
	Active   *bool
	Kind     string
	PageSize int32
}

// ListPromotions reads promotions with their redemption analytics.
func (service *Service) ListPromotions(
	ctx context.Context, filter PromotionFilter,
) ([]Promotion, error) {
	rows, err := service.queries().SearchPromotions(ctx, dbgen.SearchPromotionsParams{
		Active:   optionalBool(filter.Active),
		Kind:     optionalText(filter.Kind),
		PageSize: pageSize(filter.PageSize),
	})
	if err != nil {
		return nil, err
	}
	promotions := make([]Promotion, 0, len(rows))
	for _, row := range rows {
		promotions = append(promotions, Promotion{
			ID:              uuidString(row.Promotion.ID),
			Code:            row.Promotion.Code,
			Kind:            row.Promotion.Kind,
			Value:           row.Promotion.Value,
			Currency:        textValue(row.Promotion.Currency),
			StartsAt:        timePointer(row.Promotion.StartsAt),
			EndsAt:          timePointer(row.Promotion.EndsAt),
			RedemptionLimit: int4Pointer(row.Promotion.RedemptionLimit),
			PerCustomer:     row.Promotion.PerCustomerLimit,
			Eligibility:     row.Promotion.Eligibility,
			Active:          row.Promotion.Active,
			Stackable:       row.Promotion.Stackable,
			Precedence:      row.Promotion.Precedence,
			RedemptionCount: row.RedemptionCount,
			DiscountMinor:   row.DiscountMinor,
			CreatedAt:       timeValue(row.Promotion.CreatedAt),
		})
	}
	return promotions, nil
}

// PromotionUpdate is an editable promotion.
//
// `Kind` is absent on purpose. Changing a live promotion from a percentage
// discount into a wallet credit would silently change what every unredeemed
// code is worth; a different reward is a different promotion.
type PromotionUpdate struct {
	Value           int64
	Currency        string
	StartsAt        *time.Time
	EndsAt          *time.Time
	RedemptionLimit *int32
	PerCustomer     int32
	Eligibility     json.RawMessage
	Active          bool
	Stackable       bool
	Precedence      int32
}

// UpdatePromotion stores an edit.
func (service *Service) UpdatePromotion(
	ctx context.Context, promotionID string, update PromotionUpdate, actor Actor,
) error {
	if update.Value <= 0 || update.PerCustomer < 1 {
		return ErrValidaton
	}
	if update.EndsAt != nil && update.StartsAt != nil && !update.EndsAt.After(*update.StartsAt) {
		return ErrValidaton
	}
	id, err := parseUUID(promotionID)
	if err != nil {
		return err
	}
	eligibility := update.Eligibility
	if len(eligibility) == 0 {
		eligibility = json.RawMessage("{}")
	}

	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.UpdatePromotion(ctx, dbgen.UpdatePromotionParams{
			PromotionID:      id,
			Value:            update.Value,
			Currency:         optionalText(update.Currency),
			StartsAt:         optionalTimestamp(update.StartsAt),
			EndsAt:           optionalTimestamp(update.EndsAt),
			RedemptionLimit:  optionalInt4(update.RedemptionLimit),
			PerCustomerLimit: update.PerCustomer,
			Eligibility:      eligibility,
			Active:           update.Active,
			Stackable:        update.Stackable,
			Precedence:       update.Precedence,
		}); txErr != nil {
			return notFound(txErr)
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.promotion.updated", "marketing", "promotion", promotionID,
			map[string]any{
				"value": update.Value, "active": update.Active,
				"stackable": update.Stackable, "precedence": update.Precedence,
			},
		))
	})
}

// PromoCode is one redeemable code under a promotion.
type PromoCode struct {
	ID              string    `json:"id"`
	NormalizedCode  string    `json:"code"`
	RedemptionLimit *int32    `json:"redemptionLimit,omitempty"`
	Active          bool      `json:"active"`
	RedemptionCount int64     `json:"redemptionCount"`
	CreatedAt       time.Time `json:"createdAt"`
}

// ListPromoCodes reads the codes belonging to a promotion.
func (service *Service) ListPromoCodes(ctx context.Context, promotionID string) ([]PromoCode, error) {
	id, err := parseUUID(promotionID)
	if err != nil {
		return nil, err
	}
	rows, err := service.queries().ListPromoCodesForPromotion(ctx, id)
	if err != nil {
		return nil, err
	}
	codes := make([]PromoCode, 0, len(rows))
	for _, row := range rows {
		codes = append(codes, PromoCode{
			ID:              uuidString(row.PromoCode.ID),
			NormalizedCode:  row.PromoCode.NormalizedCode,
			RedemptionLimit: int4Pointer(row.PromoCode.RedemptionLimit),
			Active:          row.PromoCode.Active,
			RedemptionCount: row.RedemptionCount,
			CreatedAt:       timeValue(row.PromoCode.CreatedAt),
		})
	}
	return codes, nil
}

// SetPromoCodeActive enables or disables one code.
//
// Disabling stops new redemptions and leaves every completed one alone. A code
// is never deleted: the redemptions that reference it are financial records.
func (service *Service) SetPromoCodeActive(
	ctx context.Context, promoCodeID string, active bool, actor Actor,
) error {
	id, err := parseUUID(promoCodeID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.SetPromoCodeActive(ctx, dbgen.SetPromoCodeActiveParams{
			PromoCodeID: id, Active: active,
		})
		if txErr != nil {
			return notFound(txErr)
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.promo_code.updated", "marketing", "promo_code", promoCodeID,
			map[string]any{"active": active, "code": row.NormalizedCode},
		))
	})
}

// AddonSummary is one add-on as an operator sees it.
type AddonSummary struct {
	ID            string     `json:"id"`
	Code          string     `json:"code"`
	Kind          string     `json:"kind"`
	Visible       bool       `json:"visible"`
	SortOrder     int32      `json:"sortOrder"`
	LatestVersion int32      `json:"latestVersion"`
	CreatedAt     time.Time  `json:"createdAt"`
	ArchivedAt    *time.Time `json:"archivedAt,omitempty"`
}

// ListAddons reads the add-on catalogue.
func (service *Service) ListAddons(ctx context.Context) ([]AddonSummary, error) {
	rows, err := service.queries().ListAddonsAdmin(ctx)
	if err != nil {
		return nil, err
	}
	addons := make([]AddonSummary, 0, len(rows))
	for _, row := range rows {
		addons = append(addons, AddonSummary{
			ID:            uuidString(row.Addon.ID),
			Code:          row.Addon.Code,
			Kind:          row.Addon.Kind,
			Visible:       row.Addon.Visible,
			SortOrder:     row.Addon.SortOrder,
			LatestVersion: row.LatestVersion,
			CreatedAt:     timeValue(row.Addon.CreatedAt),
			ArchivedAt:    timePointer(row.Addon.ArchivedAt),
		})
	}
	return addons, nil
}

// AddonVersion is one immutable priced add-on configuration.
type AddonVersion struct {
	ID           string   `json:"id"`
	Version      int32    `json:"version"`
	TrafficBytes *int64   `json:"trafficBytes,omitempty"`
	DeviceSlots  *int32   `json:"deviceSlots,omitempty"`
	SquadIDs     []string `json:"squadIds"`
	MaxQuantity  int32    `json:"maxQuantity"`
	// Proration names the documented rule for a mid-period purchase:
	// `full_price`, `remaining_period`, or `daily_rate`. It is versioned with
	// the add-on, so a historical order is always explainable by the rule that
	// was in force when it was placed.
	Proration string           `json:"proration"`
	Prices    map[string]int64 `json:"prices"`
	CreatedAt time.Time        `json:"createdAt"`
	RetiredAt *time.Time       `json:"retiredAt,omitempty"`
}

// AddonVersions reads the priced versions of one add-on.
func (service *Service) AddonVersions(ctx context.Context, addonID string) ([]AddonVersion, error) {
	id, err := parseUUID(addonID)
	if err != nil {
		return nil, err
	}
	queries := service.queries()
	rows, err := queries.ListAddonVersions(ctx, id)
	if err != nil {
		return nil, err
	}
	versions := make([]AddonVersion, 0, len(rows))
	for _, row := range rows {
		prices, priceErr := queries.ListAddonPrices(ctx, row.ID)
		if priceErr != nil {
			return nil, priceErr
		}
		version := AddonVersion{
			ID:           uuidString(row.ID),
			Version:      row.Version,
			TrafficBytes: int8Pointer(row.TrafficBytes),
			DeviceSlots:  int4Pointer(row.DeviceSlots),
			SquadIDs:     uuidStrings(row.RemnawaveSquadIds),
			MaxQuantity:  row.MaxQuantity,
			Proration:    row.Proration,
			Prices:       make(map[string]int64, len(prices)),
			CreatedAt:    timeValue(row.CreatedAt),
			RetiredAt:    timePointer(row.RetiredAt),
		}
		for _, price := range prices {
			version.Prices[price.Currency] = price.AmountMinor
		}
		versions = append(versions, version)
	}
	return versions, nil
}
