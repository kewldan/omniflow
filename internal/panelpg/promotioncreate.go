package panelpg

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// promotionCodePattern matches promotions.code.
var promotionCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)

// NewPromotion is a promotion an operator creates from the panel, with the
// codes that redeem it.
type NewPromotion struct {
	Code string `json:"code"`
	// Kind is percent or fixed (a checkout discount), or a reward on its own:
	// wallet_credit, days, trial, traffic, balance_days, or loyalty_tier.
	Kind string `json:"kind"`
	// Value is basis points for percent, minor units for fixed and
	// wallet_credit, days for days, trial, balance_days, and loyalty_tier, and
	// GiB for traffic.
	Value    int64  `json:"value"`
	Currency string `json:"currency,omitempty"`
	// CreditMinor is the wallet part of a balance_days code.
	CreditMinor int64 `json:"creditMinor,omitempty"`
	// TierCode is the group a loyalty_tier code grants.
	TierCode string `json:"tierCode,omitempty"`
	// SquadID is the squad a squad_access code grants for value hours.
	SquadID         string     `json:"squadId,omitempty"`
	StartsAt        *time.Time `json:"startsAt,omitempty"`
	EndsAt          *time.Time `json:"endsAt,omitempty"`
	RedemptionLimit *int32     `json:"redemptionLimit,omitempty"`
	PerCustomer     int32      `json:"perCustomerLimit"`
	// PlanIDs scopes a discount to plans; empty means none — a discount with
	// no plan applies to no plan, which is how the catalogue reads it.
	PlanIDs []string `json:"planIds"`
	Codes   []string `json:"codes"`
	Active  bool     `json:"active"`
	// DiscountCooldownDays, when set, refuses a customer who had any discount
	// in that many days: "a discount at most once in sixty days".
	DiscountCooldownDays int32 `json:"discountCooldownDays,omitempty"`
}

var promotionKinds = map[string]bool{
	"percent": true, "fixed": true, "wallet_credit": true, "days": true, "trial": true,
	"traffic": true, "balance_days": true, "loyalty_tier": true, "squad_access": true,
}

// validate refuses what the CHECK constraints would refuse, so the operator
// gets a 422 they can act on rather than a database error.
func (promotion NewPromotion) validate() error {
	switch {
	case !promotionCodePattern.MatchString(promotion.Code), !promotionKinds[promotion.Kind],
		promotion.Value <= 0, promotion.PerCustomer < 1, len(promotion.Codes) == 0,
		promotion.RedemptionLimit != nil && *promotion.RedemptionLimit < 1,
		promotion.EndsAt != nil && promotion.StartsAt != nil && !promotion.EndsAt.After(*promotion.StartsAt):
		return ErrValidaton
	}
	needsCurrency := promotion.Kind == "fixed" || promotion.Kind == "wallet_credit" || promotion.Kind == "balance_days"
	if needsCurrency && len(promotion.Currency) != 3 {
		return ErrValidaton
	}
	switch promotion.Kind {
	case "percent":
		if promotion.Value > 10000 {
			return ErrValidaton
		}
	case "days", "trial", "loyalty_tier":
		if promotion.Value > 3650 {
			return ErrValidaton
		}
	case "balance_days":
		if promotion.Value > 3650 || promotion.CreditMinor <= 0 {
			return ErrValidaton
		}
	case "traffic":
		if promotion.Value > 100000 {
			return ErrValidaton
		}
	case "squad_access":
		if promotion.Value > 720 {
			return ErrValidaton
		}
	}
	if (promotion.Kind == "squad_access") != optionalUUID(promotion.SquadID).Valid {
		return ErrValidaton
	}
	if (promotion.Kind == "loyalty_tier") != (promotion.TierCode != "") {
		return ErrValidaton
	}
	if promotion.DiscountCooldownDays < 0 || promotion.DiscountCooldownDays > commerce.MaxDiscountCooldownDays {
		return ErrValidaton
	}
	for _, code := range promotion.Codes {
		if _, err := commerce.NormalizePromoCode(code); err != nil {
			return ErrValidaton
		}
	}
	return nil
}

// CreatePromotion creates a promotion and its codes from the panel. Until now
// a promotion could be created only through the bearer API.
func (service *Service) CreatePromotion(ctx context.Context, promotion NewPromotion, actor Actor) (Promotion, error) {
	promotion.Code = strings.ToLower(strings.TrimSpace(promotion.Code))
	promotion.Currency = strings.ToUpper(strings.TrimSpace(promotion.Currency))
	promotion.TierCode = strings.ToLower(strings.TrimSpace(promotion.TierCode))
	if err := promotion.validate(); err != nil {
		return Promotion{}, err
	}
	eligibility := json.RawMessage("{}")
	if promotion.DiscountCooldownDays > 0 {
		eligibility, _ = json.Marshal(map[string]any{commerce.EligibilityDiscountCooldownDays: promotion.DiscountCooldownDays})
	}
	var created Promotion
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.CreatePromotion(ctx, dbgen.CreatePromotionParams{
			Code: promotion.Code, Kind: promotion.Kind, Value: promotion.Value,
			Currency:         optionalText(promotion.Currency),
			StartsAt:         optionalTimestamp(promotion.StartsAt),
			EndsAt:           optionalTimestamp(promotion.EndsAt),
			RedemptionLimit:  optionalInt4(promotion.RedemptionLimit),
			PerCustomerLimit: promotion.PerCustomer, Eligibility: eligibility,
			Active:      promotion.Active,
			CreditMinor: pgtype.Int8{Int64: promotion.CreditMinor, Valid: promotion.Kind == "balance_days"},
			TierCode:    optionalText(promotion.TierCode),
			SquadID:     optionalUUID(promotion.SquadID),
		})
		if txErr != nil {
			return rejected(txErr)
		}
		if row.Kind != promotion.Kind || row.Value != promotion.Value {
			// The insert is an upsert on code: an existing promotion under the
			// same code is a different promotion, not this one edited.
			return ErrConflict
		}
		for _, rawID := range promotion.PlanIDs {
			planID, parseErr := parseUUID(rawID)
			if parseErr != nil {
				return ErrValidaton
			}
			if txErr = queries.AddPromotionPlan(ctx, dbgen.AddPromotionPlanParams{PromotionID: row.ID, PlanID: planID}); txErr != nil {
				return rejected(txErr)
			}
		}
		for _, rawCode := range promotion.Codes {
			code, _ := commerce.NormalizePromoCode(rawCode)
			saved, codeErr := queries.CreatePromoCode(ctx, dbgen.CreatePromoCodeParams{PromotionID: row.ID, NormalizedCode: code, Active: true})
			if codeErr != nil {
				return rejected(codeErr)
			}
			if saved.PromotionID != row.ID {
				// The code already redeems another promotion.
				return ErrConflict
			}
		}
		created = Promotion{
			ID: uuidString(row.ID), Code: row.Code, Kind: row.Kind, Value: row.Value,
			Currency: textValue(row.Currency), PerCustomer: row.PerCustomerLimit, Active: row.Active,
			Eligibility: row.Eligibility, CreatedAt: timeValue(row.CreatedAt),
			StartsAt: timePointer(row.StartsAt), EndsAt: timePointer(row.EndsAt),
			RedemptionLimit: int4Pointer(row.RedemptionLimit),
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.promotion.created", "marketing", "promotion", created.ID,
			map[string]any{"code": row.Code, "kind": row.Kind, "value": row.Value, "codes": len(promotion.Codes)},
		))
	})
	return created, err
}
