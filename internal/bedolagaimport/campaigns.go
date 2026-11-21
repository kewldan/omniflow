package bedolagaimport

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

// Campaign is a Bedolaga advertising campaign: a /start parameter and what
// a newcomer through it was given.
type Campaign struct {
	ID             int64
	Name           string
	StartParameter string
	// BonusType is balance, subscription, tariff, or none.
	BonusType          string
	BalanceBonusKopeks int64
	SubscriptionDays   int64
	TariffDays         int64
	Active             bool
}

// CampaignRegistration is one user who arrived through a campaign.
type CampaignRegistration struct {
	ID         int64
	CampaignID int64
	UserID     int64
	CreatedAt  time.Time
}

// StarsOrder is a Telegram Stars purchase the previous bot's shop delivered.
type StarsOrder struct {
	ID           int64
	UserID       int64
	Quantity     int64
	AmountKopeks int64
	DeliveredAt  time.Time
}

// Codes of the campaign and goods decisions.
const (
	CodeInvalidCampaignCode = "invalid_campaign_code"
	CodeDuplicateCampaign   = "duplicate_campaign_code"
	CodeCampaignNotCarried  = "campaign_not_carried"
	CodeEarlierCampaign     = "earlier_campaign_registration"
	CodeAlreadyInCampaign   = "already_in_campaign"
	// CodeTariffBonusAsDays is a warning: a tariff bonus is carried as days
	// on the trial plan, since Omniflow's plans are not Bedolaga's tariffs.
	CodeTariffBonusAsDays = "tariff_bonus_as_days"
	// CodeCampaignBonusDropped is a warning: the bonus has no Omniflow
	// equivalent within its bounds and the campaign is carried for tracking.
	CodeCampaignBonusDropped = "campaign_bonus_dropped"
)

// CampaignDecision carries a Bedolaga campaign over by its /start code, or
// matches it to an Omniflow campaign that already has the code.
type CampaignDecision struct {
	Source   Campaign
	Action   Action
	Codes    []string
	Warnings []string
	// Code is the Omniflow campaign code: the start parameter, lower-cased.
	Code       string
	BonusKind  string
	BonusValue int64
}

// AttributionDecision attributes a carried customer to the campaign they
// first arrived through, with the original date. The bonus was given in
// Bedolaga and is not given again.
type AttributionDecision struct {
	Source     CampaignRegistration
	Action     Action
	Codes      []string
	Code       string
	CustomerID string
}

// GoodsDecision carries a delivered Stars order over as shop history.
type GoodsDecision struct {
	Source   StarsOrder
	Action   Action
	Codes    []string
	Currency string
}

// campaignCodePattern is ad_campaigns_code_shape.
var campaignCodePattern = regexp.MustCompile(`^[a-z0-9_]{2,40}$`)

// campaignBonus maps a Bedolaga bonus onto an Omniflow one.
func campaignBonus(campaign Campaign) (kind string, value int64, warnings []string) {
	days := func(count int64) (string, int64, []string) {
		if count < 1 || count > 365 {
			return "none", 0, []string{CodeCampaignBonusDropped}
		}
		return "days", count, nil
	}
	switch campaign.BonusType {
	case "balance":
		if campaign.BalanceBonusKopeks <= 0 {
			return "none", 0, nil
		}
		return "wallet", campaign.BalanceBonusKopeks, nil
	case "subscription":
		return days(campaign.SubscriptionDays)
	case "tariff":
		kind, value, warnings := days(campaign.TariffDays)
		if kind == "days" {
			warnings = []string{CodeTariffBonusAsDays}
		}
		return kind, value, warnings
	default:
		return "none", 0, nil
	}
}

func decideCampaigns(plan *Plan, source Source, target Target, userIndex map[int64]int) {
	campaigns := append([]Campaign(nil), source.Campaigns...)
	sort.Slice(campaigns, func(i, j int) bool { return campaigns[i].ID < campaigns[j].ID })
	owners := map[string]int{}
	for _, campaign := range campaigns {
		owners[strings.ToLower(strings.TrimSpace(campaign.StartParameter))]++
	}
	carried := map[int64]string{}
	for _, campaign := range campaigns {
		code := strings.ToLower(strings.TrimSpace(campaign.StartParameter))
		decision := CampaignDecision{Source: campaign, Code: code}
		decision.BonusKind, decision.BonusValue, decision.Warnings = campaignBonus(campaign)
		switch {
		case !campaignCodePattern.MatchString(code):
			decision.Action, decision.Codes = ActionSkip, []string{CodeInvalidCampaignCode}
		case owners[code] > 1:
			decision.Action, decision.Codes = ActionConflict, []string{CodeDuplicateCampaign}
		case target.AdCampaigns[code]:
			// An Omniflow campaign with the code is the same link; it is
			// matched, and what the operator configured there stands.
			decision.Action, decision.Warnings = ActionUnchanged, nil
		default:
			decision.Action = ActionCreate
		}
		if decision.Action == ActionCreate || decision.Action == ActionUnchanged {
			carried[campaign.ID] = code
		}
		plan.Campaigns = append(plan.Campaigns, decision)
	}

	registrations := append([]CampaignRegistration(nil), source.CampaignRegistrations...)
	sort.Slice(registrations, func(i, j int) bool {
		if !registrations[i].CreatedAt.Equal(registrations[j].CreatedAt) {
			return registrations[i].CreatedAt.Before(registrations[j].CreatedAt)
		}
		return registrations[i].ID < registrations[j].ID
	})
	first := map[int64]bool{}
	for _, registration := range registrations {
		decision := AttributionDecision{Source: registration, Code: carried[registration.CampaignID]}
		var user *UserDecision
		if index, ok := userIndex[registration.UserID]; ok {
			user = &plan.Users[index]
			decision.CustomerID = user.CustomerID
		}
		switch {
		case user == nil || !user.Importable():
			decision.Action, decision.Codes = ActionSkip, []string{CodeUserNotImported}
		case decision.Code == "":
			decision.Action, decision.Codes = ActionSkip, []string{CodeCampaignNotCarried}
		case first[registration.UserID]:
			// A customer is attributed to one campaign, the first that
			// brought them, as a sign-up through Omniflow is.
			decision.Action, decision.Codes = ActionSkip, []string{CodeEarlierCampaign}
		case user.CustomerID != "" && target.CampaignAttributions[user.CustomerID] != "":
			if target.CampaignAttributions[user.CustomerID] == decision.Code {
				decision.Action = ActionUnchanged
			} else {
				decision.Action, decision.Codes = ActionSkip, []string{CodeAlreadyInCampaign}
			}
		default:
			decision.Action = ActionCreate
		}
		if user != nil && user.Importable() && decision.Code != "" {
			first[registration.UserID] = true
		}
		plan.Attributions = append(plan.Attributions, decision)
	}
}

func decideGoods(plan *Plan, source Source, target Target, mapping Mapping, userIndex map[int64]int) {
	orders := append([]StarsOrder(nil), source.StarsOrders...)
	sort.Slice(orders, func(i, j int) bool { return orders[i].ID < orders[j].ID })
	for _, order := range orders {
		decision := GoodsDecision{Source: order, Currency: mapping.Currency}
		index, ok := userIndex[order.UserID]
		switch {
		case !ok || !plan.Users[index].Importable():
			decision.Action, decision.Codes = ActionSkip, []string{CodeUserNotImported}
		case order.AmountKopeks <= 0 || order.Quantity <= 0:
			decision.Action, decision.Codes = ActionSkip, []string{CodeNonPositiveAmount}
		case target.LegacyGoods[order.ID]:
			decision.Action = ActionUnchanged
		default:
			decision.Action = ActionCreate
		}
		plan.Goods = append(plan.Goods, decision)
	}
}
