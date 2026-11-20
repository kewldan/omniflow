package panelpg

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// PlanVersionInput is a complete new version of a plan.
//
// Every field the customer's entitlement depends on is here, because a version
// is created whole. There is no partial edit: a plan version is immutable once
// an order references it, so changing a price means publishing the next version
// and letting the old one keep pricing the orders that already used it.
type PlanVersionInput struct {
	BillingPeriod   string
	DurationSeconds int64
	// TrafficAllowanceBytes and DeviceLimit are unlimited when nil, which is a
	// different thing from zero — zero would mean a plan that permits nothing.
	TrafficAllowanceBytes *int64
	DeviceLimit           *int32
	SquadIDs              []string
	SquadSelection        string
	MinSelectableSquads   int32
	MaxSelectableSquads   *int32
	UpgradePolicy         string
	DowngradePolicy       string
	CancellationPolicy    string
	GracePeriodSeconds    int64
	TrialEligibility      string
	RecurringCapable      bool
	// Prices are keyed by ISO currency. A version with no price cannot be
	// bought, so at least one is required.
	Prices map[string]int64
	// AddonIDs are the add-ons offered alongside this version.
	AddonIDs []string
	PlanVersionOptions
}

// PlanVersionOptions are the version fields that arrived after the first
// editor. They carry their wire names, so the panel route decodes them
// straight into the input and a new option is one field here rather than one
// in three places.
type PlanVersionOptions struct {
	// OfferedSquads are the squads a customer may choose when SquadSelection is
	// `optional` or `required`. SquadIDs stay the squads every subscription on
	// the version is given whatever the customer picks.
	OfferedSquads []OfferedSquad `json:"offeredSquads"`
	// TrafficResetStrategy is Remnawave's trafficLimitStrategy for users on
	// this version: when the panel resets the traffic counter on its own.
	// Empty is NO_RESET.
	TrafficResetStrategy string `json:"trafficResetStrategy"`
	// ExternalSquadID is the Remnawave external squad that shapes the
	// subscription page of users on this version. Empty sets none.
	ExternalSquadID string `json:"externalSquadId"`
	// MaxAddonTrafficBytes caps the traffic add-ons may add to one
	// entitlement of this version; nil is no cap.
	MaxAddonTrafficBytes *int64 `json:"maxAddonTrafficBytes"`
	// TrafficResetPriceBps sells a customer a reset of the traffic counter at
	// this share of the version's own price, in basis points; nil sells none.
	TrafficResetPriceBps *int32 `json:"trafficResetPriceBps"`
	// Periods are the further billing periods the version is sold for, each
	// priced per currency; CustomDaysMin and CustomDaysMax bound a number of
	// days the customer chooses, priced per day by DayPrices.
	Periods       []PlanPeriod     `json:"periods"`
	CustomDaysMin *int32           `json:"customDaysMin"`
	CustomDaysMax *int32           `json:"customDaysMax"`
	DayPrices     map[string]int64 `json:"dayPrices"`
}

var validTrafficResetStrategy = map[string]bool{
	"NO_RESET": true, "DAY": true, "WEEK": true, "MONTH": true, "MONTH_ROLLING": true,
}

// OfferedSquad is one squad a customer may choose, with the label each
// language shows for it.
type OfferedSquad struct {
	SquadID string `json:"squadId"`
	LabelRU string `json:"labelRu"`
	LabelEN string `json:"labelEn"`
}

// The accepted values are exactly the ones the `plan_versions` CHECK
// constraints allow and internal/commerce implements. The editor once sent
// `customer_choice` and `fixed` for the squad selection, which nothing
// implements and the database refused, so a choosable squad set could not be
// published from the panel at all.
var (
	validBillingPeriods = map[string]bool{
		"none": true, "day": true, "week": true, "month": true, "quarter": true,
		"half_year": true, "year": true, "custom": true,
	}
	validSquadSelection = map[string]bool{
		"automatic": true, "optional": true, "required": true,
	}
	validUpgradePolicy      = map[string]bool{"forbid": true, "replace": true, "extend": true}
	validDowngradePolicy    = map[string]bool{"forbid": true, "immediate": true, "at_expiry": true}
	validCancellationPolicy = map[string]bool{"forbid": true, "immediate": true, "at_expiry": true}
	validTrialEligibility   = map[string]bool{
		"new_customer": true, "never_trialled": true, "any": true,
	}
	validProration = map[string]bool{
		"full_price": true, "remaining_period": true, "daily_rate": true,
	}
)

// CreatePlanVersion publishes the next version of a plan.
//
// Validation happens here rather than only in the database because the CHECK
// constraints say what is representable, not what is sensible: a plan priced in
// no currency and a plan whose minimum selectable squad count exceeds its
// maximum are both storable and both broken.
func (service *Service) CreatePlanVersion(
	ctx context.Context, planID string, input PlanVersionInput, actor Actor,
) (PlanVersion, error) {
	id, err := parseUUID(planID)
	if err != nil {
		return PlanVersion{}, err
	}
	if err := validatePlanVersion(input); err != nil {
		return PlanVersion{}, err
	}
	squadIDs, err := databaseutil.ParseUUIDs(input.SquadIDs)
	if err != nil {
		return PlanVersion{}, ErrValidaton
	}

	var created PlanVersion
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		// The advisory lock inside NextPlanVersion serialises concurrent
		// publishes of the same plan, so two operators cannot mint version 4
		// twice.
		next, txErr := queries.NextPlanVersion(ctx, id)
		if txErr != nil {
			return txErr
		}
		row, txErr := queries.CreatePlanVersionFull(ctx, dbgen.CreatePlanVersionFullParams{
			PlanID: id, Version: next,
			BillingPeriod:         input.BillingPeriod,
			DurationSeconds:       input.DurationSeconds,
			TrafficAllowanceBytes: optionalInt8(input.TrafficAllowanceBytes),
			DeviceLimit:           optionalInt4(input.DeviceLimit),
			RemnawaveSquadIds:     squadIDs,
			SquadSelection:        input.SquadSelection,
			MinSelectableSquads:   input.MinSelectableSquads,
			MaxSelectableSquads:   optionalInt4(input.MaxSelectableSquads),
			UpgradePolicy:         input.UpgradePolicy,
			DowngradePolicy:       input.DowngradePolicy,
			CancellationPolicy:    input.CancellationPolicy,
			GracePeriodSeconds:    input.GracePeriodSeconds,
			TrialEligibility:      input.TrialEligibility,
			RecurringCapable:      input.RecurringCapable,
		})
		if txErr != nil {
			return txErr
		}
		for currency, amount := range input.Prices {
			if _, txErr = queries.CreatePlanPrice(ctx, dbgen.CreatePlanPriceParams{
				PlanVersionID: row.ID, Currency: strings.ToUpper(currency), AmountMinor: amount,
			}); txErr != nil {
				return txErr
			}
		}
		// The offered add-on set is replaced rather than merged, so it is
		// exactly what the operator chose rather than the union of every edit.
		if txErr = queries.SetPlanVersionAddons(ctx, row.ID); txErr != nil {
			return txErr
		}
		for _, addonID := range input.AddonIDs {
			parsed, parseErr := parseUUID(addonID)
			if parseErr != nil {
				return ErrValidaton
			}
			if txErr = queries.OfferAddonWithPlanVersion(ctx, dbgen.OfferAddonWithPlanVersionParams{
				PlanVersionID: row.ID, AddonID: parsed,
			}); txErr != nil {
				return txErr
			}
		}
		for position, squad := range input.OfferedSquads {
			parsed, parseErr := parseUUID(strings.TrimSpace(squad.SquadID))
			if parseErr != nil {
				return ErrValidaton
			}
			if txErr = queries.InsertPlanVersionSquad(ctx, dbgen.InsertPlanVersionSquadParams{
				PlanVersionID: row.ID, SquadID: parsed,
				LabelRu: strings.TrimSpace(squad.LabelRU), LabelEn: strings.TrimSpace(squad.LabelEN),
				SortOrder: int32(position),
			}); txErr != nil {
				return txErr
			}
		}
		strategy := input.TrafficResetStrategy
		if strategy == "" {
			strategy = "NO_RESET"
		}
		externalSquad := pgtype.UUID{}
		if input.ExternalSquadID != "" {
			if externalSquad, txErr = parseUUID(input.ExternalSquadID); txErr != nil {
				return ErrValidaton
			}
		}
		if txErr = queries.SetPlanVersionRemnawaveProfile(ctx, dbgen.SetPlanVersionRemnawaveProfileParams{
			PlanVersionID: row.ID, TrafficResetStrategy: strategy, ExternalSquadID: externalSquad,
		}); txErr != nil {
			return txErr
		}
		if txErr = writePeriods(ctx, queries, row.ID, input); txErr != nil {
			return txErr
		}
		if txErr = queries.SetPlanVersionTrafficResetPrice(ctx, dbgen.SetPlanVersionTrafficResetPriceParams{
			PlanVersionID: row.ID, TrafficResetPriceBps: optionalInt4(input.TrafficResetPriceBps),
		}); txErr != nil {
			return txErr
		}
		row.TrafficResetPriceBps = optionalInt4(input.TrafficResetPriceBps)
		if txErr = queries.SetPlanVersionAddonTrafficCap(ctx, dbgen.SetPlanVersionAddonTrafficCapParams{
			PlanVersionID: row.ID, MaxAddonTrafficBytes: optionalInt8(input.MaxAddonTrafficBytes),
		}); txErr != nil {
			return txErr
		}
		row.TrafficResetStrategy, row.ExternalSquadID = strategy, externalSquad
		row.MaxAddonTrafficBytes = optionalInt8(input.MaxAddonTrafficBytes)
		created = planVersionFromRow(row, input.Prices)
		created.OfferedSquads = input.OfferedSquads
		created.AddonIDs = input.AddonIDs
		return appendAudit(ctx, queries, actor.audit(
			"panel.plan_version.created", "configuration", "plan", planID,
			map[string]any{
				"version": next, "billingPeriod": input.BillingPeriod,
				"durationSeconds":  input.DurationSeconds,
				"currencies":       currencyList(input.Prices),
				"squadSelection":   input.SquadSelection,
				"trialEligibility": input.TrialEligibility,
			},
		))
	})
	return created, err
}

// validatePlanVersion refuses the combinations that are storable and broken.
func validatePlanVersion(input PlanVersionInput) error {
	switch {
	case !validBillingPeriods[input.BillingPeriod],
		!validSquadSelection[input.SquadSelection],
		!validUpgradePolicy[input.UpgradePolicy],
		!validDowngradePolicy[input.DowngradePolicy],
		!validCancellationPolicy[input.CancellationPolicy],
		!validTrialEligibility[input.TrialEligibility]:
		return ErrValidaton
	case input.DurationSeconds <= 0, input.GracePeriodSeconds < 0:
		return ErrValidaton
	case len(input.Prices) == 0:
		// A version nobody can buy is not a version.
		return ErrValidaton
	case input.TrafficAllowanceBytes != nil && *input.TrafficAllowanceBytes <= 0:
		return ErrValidaton
	case input.DeviceLimit != nil && *input.DeviceLimit <= 0:
		return ErrValidaton
	}
	for _, amount := range input.Prices {
		if amount < 0 {
			return ErrValidaton
		}
	}
	if err := validatePeriods(input); err != nil {
		return err
	}
	if input.TrafficResetPriceBps != nil && (*input.TrafficResetPriceBps < 1 || *input.TrafficResetPriceBps > 100_000) {
		return ErrValidaton
	}
	if input.MaxAddonTrafficBytes != nil && *input.MaxAddonTrafficBytes <= 0 {
		return ErrValidaton
	}
	if input.TrafficResetStrategy != "" && !validTrafficResetStrategy[input.TrafficResetStrategy] {
		return ErrValidaton
	}
	if input.ExternalSquadID != "" {
		if _, err := parseUUID(input.ExternalSquadID); err != nil {
			return ErrValidaton
		}
	}
	return validateSquadChoice(input)
}

// validateSquadChoice refuses an offered squad set the checkout could never
// satisfy. A choice needs something to choose from; a required choice needs a
// minimum of at least one; a maximum below the minimum, or above what is
// offered, describes a selection that can never be made. An automatic version
// offers nothing, because the checkout refuses any selection against it.
func validateSquadChoice(input PlanVersionInput) error {
	if input.MinSelectableSquads < 0 || input.MaxSelectableSquads != nil && *input.MaxSelectableSquads < 1 {
		return ErrValidaton
	}
	if input.SquadSelection == "automatic" {
		if len(input.OfferedSquads) > 0 {
			return ErrValidaton
		}
		return nil
	}
	offered := len(input.OfferedSquads)
	if offered == 0 {
		return ErrValidaton
	}
	if input.SquadSelection == "required" && input.MinSelectableSquads < 1 {
		return ErrValidaton
	}
	if int(input.MinSelectableSquads) > offered {
		return ErrValidaton
	}
	if input.MaxSelectableSquads != nil && *input.MaxSelectableSquads < input.MinSelectableSquads {
		return ErrValidaton
	}
	seen := make(map[string]bool, offered)
	for _, squad := range input.OfferedSquads {
		id := strings.ToLower(strings.TrimSpace(squad.SquadID))
		labelRU, labelEN := strings.TrimSpace(squad.LabelRU), strings.TrimSpace(squad.LabelEN)
		// Each label is shown verbatim in its language; a blank one is an empty
		// checkbox on the customer's screen, and the column caps both at 80.
		if id == "" || seen[id] || labelRU == "" || labelEN == "" ||
			len([]rune(labelRU)) > 80 || len([]rune(labelEN)) > 80 {
			return ErrValidaton
		}
		seen[id] = true
	}
	return nil
}

// AddonInput is a complete add-on together with its first version.
type AddonInput struct {
	Code          string
	Kind          string
	Visible       bool
	SortOrder     int32
	NameEN        string
	NameRU        string
	DescriptionEN string
	DescriptionRU string
	// The payload is what the add-on grants, and exactly one kind of it is
	// meaningful: traffic bytes, device slots, or squads.
	TrafficBytes *int64
	DeviceSlots  *int32
	SquadIDs     []string
	MaxQuantity  int32
	// Proration decides what a mid-period purchase costs. `full_price` charges
	// the whole amount whatever is left of the period, `remaining_period`
	// scales it, and `daily_rate` prices by day.
	Proration string
	// LifetimeDays is how long a traffic pack lasts from its purchase; nil
	// lasts as long as the subscription period it was added to.
	LifetimeDays *int32
	Prices       map[string]int64
}

// SaveAddon creates or updates an add-on and publishes a new version of it.
//
// The add-on row carries presentation and is mutable; the version carries what
// the customer gets and its price, and is not. That split is the same one plans
// use, and for the same reason: a historical order must keep costing what it
// cost.
func (service *Service) SaveAddon(
	ctx context.Context, input AddonInput, actor Actor,
) (string, error) {
	if err := validateAddon(input); err != nil {
		return "", err
	}
	squadIDs, err := databaseutil.ParseUUIDs(input.SquadIDs)
	if err != nil {
		return "", ErrValidaton
	}

	var addonID string
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		addon, txErr := queries.UpsertAddon(ctx, dbgen.UpsertAddonParams{
			Code: strings.ToLower(strings.TrimSpace(input.Code)), Kind: input.Kind,
			Visible: input.Visible, SortOrder: input.SortOrder,
		})
		if txErr != nil {
			return txErr
		}
		addonID = uuidString(addon.ID)
		for locale, copy := range map[string][2]string{
			"en": {input.NameEN, input.DescriptionEN},
			"ru": {input.NameRU, input.DescriptionRU},
		} {
			if strings.TrimSpace(copy[0]) == "" {
				// A missing translation is refused rather than stored blank: an
				// add-on with no name in one language is an empty row on the
				// customer's screen.
				return ErrValidaton
			}
			if _, txErr = queries.UpsertAddonLocalization(ctx, dbgen.UpsertAddonLocalizationParams{
				AddonID: addon.ID, Locale: locale, Name: copy[0], Description: copy[1],
			}); txErr != nil {
				return txErr
			}
		}
		next, txErr := queries.NextAddonVersion(ctx, addon.ID)
		if txErr != nil {
			return txErr
		}
		version, txErr := queries.CreateAddonVersion(ctx, dbgen.CreateAddonVersionParams{
			AddonID: addon.ID, Version: next,
			TrafficBytes:      optionalInt8(input.TrafficBytes),
			DeviceSlots:       optionalInt4(input.DeviceSlots),
			RemnawaveSquadIds: squadIDs,
			MaxQuantity:       input.MaxQuantity,
			Proration:         input.Proration,
		})
		if txErr != nil {
			return txErr
		}
		if txErr = queries.SetAddonVersionLifetime(ctx, dbgen.SetAddonVersionLifetimeParams{
			AddonVersionID: version.ID, LifetimeDays: optionalInt4(input.LifetimeDays),
		}); txErr != nil {
			return txErr
		}
		for currency, amount := range input.Prices {
			if _, txErr = queries.CreateAddonPrice(ctx, dbgen.CreateAddonPriceParams{
				AddonVersionID: version.ID, Currency: strings.ToUpper(currency), AmountMinor: amount,
			}); txErr != nil {
				return txErr
			}
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.addon.saved", "configuration", "addon", addonID,
			map[string]any{
				"code": input.Code, "kind": input.Kind, "version": next,
				"proration": input.Proration, "currencies": currencyList(input.Prices),
			},
		))
	})
	return addonID, err
}

func validateAddon(input AddonInput) error {
	switch {
	case strings.TrimSpace(input.Code) == "",
		!validProration[input.Proration],
		input.MaxQuantity < 1,
		len(input.Prices) == 0:
		return ErrValidaton
	}
	// Only a traffic pack runs out: slots and squads last the period.
	if input.LifetimeDays != nil && (*input.LifetimeDays < 1 || input.Kind != "traffic") {
		return ErrValidaton
	}
	switch input.Kind {
	case "traffic":
		if input.TrafficBytes == nil || *input.TrafficBytes <= 0 {
			return ErrValidaton
		}
	case "devices":
		if input.DeviceSlots == nil || *input.DeviceSlots <= 0 {
			return ErrValidaton
		}
	case "squads":
		if len(input.SquadIDs) == 0 {
			return ErrValidaton
		}
	default:
		return ErrValidaton
	}
	return nil
}

// RetireAddonVersion stops a version being sold without deleting it.
func (service *Service) RetireAddonVersion(
	ctx context.Context, versionID string, actor Actor,
) error {
	id, err := parseUUID(versionID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.RetireAddonVersion(ctx, id); txErr != nil {
			return notFound(txErr)
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.addon_version.retired", "configuration", "addon_version", versionID, nil,
		))
	})
}

// planVersionFromRow renders a freshly created version for the response.
func planVersionFromRow(row dbgen.PlanVersion, prices map[string]int64) PlanVersion {
	normalized := make(map[string]int64, len(prices))
	for currency, amount := range prices {
		normalized[strings.ToUpper(currency)] = amount
	}
	return PlanVersion{
		ID: uuidString(row.ID), Version: row.Version,
		BillingPeriod: row.BillingPeriod, DurationSeconds: row.DurationSeconds,
		TrafficAllowance:    int8Pointer(row.TrafficAllowanceBytes),
		DeviceLimit:         int4Pointer(row.DeviceLimit),
		SquadIDs:            databaseutil.UUIDStrings(row.RemnawaveSquadIds),
		SquadSelection:      row.SquadSelection,
		MinSelectableSquads: row.MinSelectableSquads,
		MaxSelectableSquads: int4Pointer(row.MaxSelectableSquads),
		UpgradePolicy:       row.UpgradePolicy,
		DowngradePolicy:     row.DowngradePolicy,
		CancellationPolicy:  row.CancellationPolicy,
		GracePeriodSeconds:  row.GracePeriodSeconds,
		TrialEligibility:    row.TrialEligibility,
		RecurringCapable:    row.RecurringCapable,
		Prices:              normalized,
		CreatedAt:           timeValue(row.CreatedAt),

		TrafficResetStrategy: row.TrafficResetStrategy,
		MaxAddonTrafficBytes: int8Pointer(row.MaxAddonTrafficBytes),
		TrafficResetPriceBps: int4Pointer(row.TrafficResetPriceBps),
		ExternalSquadID:      uuidString(row.ExternalSquadID),
	}
}

// currencyList renders the priced currencies for the audit record, sorted so
// two identical publishes produce identical audit metadata.
func currencyList(prices map[string]int64) []string {
	currencies := make([]string, 0, len(prices))
	for currency := range prices {
		currencies = append(currencies, strings.ToUpper(currency))
	}
	sortStrings(currencies)
	return currencies
}

func sortStrings(values []string) {
	for index := 1; index < len(values); index++ {
		for position := index; position > 0 && values[position] < values[position-1]; position-- {
			values[position], values[position-1] = values[position-1], values[position]
		}
	}
}
