package panelpg

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// AdCampaign is a /start campaign and what it produced.
type AdCampaign struct {
	ID         string `json:"id"`
	Code       string `json:"code"`
	Name       string `json:"name"`
	BonusKind  string `json:"bonusKind"`
	BonusValue int64  `json:"bonusValue"`
	// BonusPlanID is the plan a "plan" bonus grants days of; BonusPlanCode
	// names it in the listing.
	BonusPlanID   string    `json:"bonusPlanId,omitempty"`
	BonusPlanCode string    `json:"bonusPlanCode,omitempty"`
	Currency      string    `json:"currency"`
	Active        bool      `json:"active"`
	CreatedAt     time.Time `json:"createdAt"`
	// Registrations are customers the campaign brought; Trials those of them
	// who took a trial; Paying those with a settled subscription order; and
	// RevenueMinor what those orders settled, in the campaign currency only.
	Registrations int64 `json:"registrations"`
	Trials        int64 `json:"trials"`
	Paying        int64 `json:"paying"`
	RevenueMinor  int64 `json:"revenueMinor"`
}

var adCampaignCode = regexp.MustCompile(`^[a-z0-9_]{2,40}$`)

// AdCampaigns lists every campaign with its results.
func (service *Service) AdCampaigns(ctx context.Context) ([]AdCampaign, error) {
	rows, err := service.pool.Query(ctx, `SELECT c.id::text, c.code, c.name, c.bonus_kind, c.bonus_value,
		COALESCE(c.bonus_plan_id::text, ''), COALESCE(p.code, ''), c.currency, c.active, c.created_at,
		(SELECT count(*) FROM customer_campaign_attributions a WHERE a.campaign_id = c.id),
		(SELECT count(*) FROM customer_campaign_attributions a JOIN trial_claims t ON t.user_id = a.user_id WHERE a.campaign_id = c.id),
		(SELECT count(DISTINCT o.user_id) FROM customer_campaign_attributions a JOIN orders o ON o.user_id = a.user_id
			WHERE a.campaign_id = c.id AND o.state IN ('paid','fulfilled','partially_refunded')
			  AND o.operation IN ('purchase','extension','renewal','upgrade','downgrade') AND o.paid_minor > 0),
		(SELECT COALESCE(sum(o.paid_minor - o.refunded_minor), 0)::bigint FROM customer_campaign_attributions a JOIN orders o ON o.user_id = a.user_id
			WHERE a.campaign_id = c.id AND o.currency = c.currency AND o.state IN ('paid','fulfilled','partially_refunded','refunded'))
		FROM ad_campaigns c LEFT JOIN plans p ON p.id = c.bonus_plan_id ORDER BY c.created_at DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (AdCampaign, error) {
		var campaign AdCampaign
		err := row.Scan(&campaign.ID, &campaign.Code, &campaign.Name, &campaign.BonusKind, &campaign.BonusValue,
			&campaign.BonusPlanID, &campaign.BonusPlanCode, &campaign.Currency, &campaign.Active, &campaign.CreatedAt,
			&campaign.Registrations, &campaign.Trials, &campaign.Paying, &campaign.RevenueMinor)
		return campaign, err
	})
}

// SaveAdCampaign creates a campaign or, for an existing code, updates its
// name, bonus, and whether it is active. The code is the link and never
// changes once customers have arrived through it.
func (service *Service) SaveAdCampaign(ctx context.Context, campaign AdCampaign, actor Actor) (AdCampaign, error) {
	campaign.Code = strings.ToLower(strings.TrimSpace(campaign.Code))
	campaign.Name = strings.TrimSpace(campaign.Name)
	campaign.Currency = strings.ToUpper(strings.TrimSpace(campaign.Currency))
	if campaign.Currency == "" {
		campaign.Currency = "RUB"
	}
	if campaign.BonusKind == "" {
		campaign.BonusKind = "none"
	}
	if campaign.BonusKind == "none" {
		campaign.BonusValue = 0
	}
	if campaign.BonusKind != "plan" {
		campaign.BonusPlanID = ""
	}
	days := campaign.BonusValue >= 1 && campaign.BonusValue <= 365
	valid := adCampaignCode.MatchString(campaign.Code) && campaign.Name != "" && len([]rune(campaign.Name)) <= 120 &&
		(campaign.BonusKind == "none" || campaign.BonusKind == "wallet" && campaign.BonusValue > 0 ||
			(campaign.BonusKind == "days" || campaign.BonusKind == "trial") && days ||
			campaign.BonusKind == "plan" && days && campaign.BonusPlanID != "")
	if !valid {
		return AdCampaign{}, ErrValidaton
	}
	planID := pgtype.UUID{}
	if campaign.BonusPlanID != "" {
		parsed, err := parseUUID(campaign.BonusPlanID)
		if err != nil {
			return AdCampaign{}, ErrValidaton
		}
		planID = parsed
	}
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, err := queries.SaveAdCampaign(ctx, dbgen.SaveAdCampaignParams{
			Code: campaign.Code, Name: campaign.Name, BonusKind: campaign.BonusKind,
			BonusValue: campaign.BonusValue, BonusPlanID: planID, Currency: campaign.Currency, Active: campaign.Active,
		}); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && (pgErr.Code == "23503" || pgErr.Code == "23514") {
				return ErrValidaton
			}
			return err
		}
		return appendAudit(ctx, queries, actor.audit(
			"marketing.ad_campaign.saved", "marketing", "ad_campaign", campaign.Code,
			map[string]any{"bonusKind": campaign.BonusKind, "bonusValue": campaign.BonusValue,
				"bonusPlanId": campaign.BonusPlanID, "active": campaign.Active},
		))
	})
	return campaign, err
}
