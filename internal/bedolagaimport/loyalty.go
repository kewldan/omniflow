package bedolagaimport

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// PromoGroup is a Bedolaga promo group: a discount a customer holds, given by
// an operator or earned automatically past a total spend.
type PromoGroup struct {
	ID              int64
	Name            string
	Priority        int64
	ServerPercent   int64
	TrafficPercent  int64
	DevicePercent   int64
	HasPeriodRules  bool
	AutoSpentKopeks *int64
	IsDefault       bool
}

// PromoMember is one customer in a promo group.
type PromoMember struct {
	UserID     int64
	GroupID    int64
	AssignedAt time.Time
	AssignedBy string
}

// Codes of the loyalty decisions.
const (
	// CodeLoyaltyProgramExists: Omniflow already has a loyalty programme,
	// which the operator configured; the groups are not laid over it.
	CodeLoyaltyProgramExists = "loyalty_program_exists"
	CodeNoDiscountGroups     = "no_discount_groups"
	CodeDefaultGroup         = "default_group"
	CodeTierNotCarried       = "tier_not_carried"
	// CodePeriodDiscountsDropped is a warning: per-period discounts are not
	// carried; the base discount is.
	CodePeriodDiscountsDropped = "period_discounts_dropped"
)

// LoyaltyTier is an Omniflow tier made from a promo group.
type LoyaltyTier struct {
	SourceID           int64
	Code               string
	Name               string
	Threshold          int64
	DiscountBPS        int32
	TrafficDiscountBPS int32
	DeviceDiscountBPS  int32
	Priority           int32
	ManualOnly         bool
	SortOrder          int32
}

// LoyaltyDecision carries the promo groups over as a loyalty programme: spend
// over (nearly) a customer's lifetime, as Bedolaga's total spend was, with a
// tier per group. It is made only when Omniflow has no programme yet.
type LoyaltyDecision struct {
	Action     Action
	Codes      []string
	Warnings   []string
	WindowDays int32
	Tiers      []LoyaltyTier
}

// MembershipDecision keeps one customer in the tier their promo group became,
// whether an operator put them there or Bedolaga did by their spend, so the
// discount does not lapse while the programme first evaluates them.
type MembershipDecision struct {
	Source     PromoMember
	Action     Action
	Codes      []string
	CustomerID string
	TierCode   string
	Note       string
}

// loyaltyWindowDays stands in for "ever": Bedolaga counted total spend.
const loyaltyWindowDays = 3650

var tierCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

// tierCode makes a tier code from a group's name: its Latin letters and
// digits, or "group-<id>" for a name without them.
func tierCode(group PromoGroup) string {
	var builder strings.Builder
	for _, character := range strings.ToLower(group.Name) {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
			builder.WriteRune(character)
		case character == ' ' || character == '-' || character == '_':
			if builder.Len() > 0 && !strings.HasSuffix(builder.String(), "-") {
				builder.WriteRune('-')
			}
		}
	}
	code := strings.Trim(builder.String(), "-")
	if len(code) > 32 {
		code = strings.Trim(code[:32], "-")
	}
	if !tierCodePattern.MatchString(code) {
		code = "group-" + strconv.FormatInt(group.ID, 10)
	}
	return code
}

func percentBPS(percent int64) int32 {
	return int32(min(max(percent, 0), 100) * 100)
}

func decideLoyalty(plan *Plan, source Source, target Target, userIndex map[int64]int) {
	groups := append([]PromoGroup(nil), source.PromoGroups...)
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	decision := LoyaltyDecision{WindowDays: loyaltyWindowDays}
	codes := map[int64]string{}
	used := map[string]bool{}
	discounted := false
	for _, group := range groups {
		code := tierCode(group)
		if used[code] {
			code = "group-" + strconv.FormatInt(group.ID, 10)
		}
		used[code] = true
		codes[group.ID] = code
		tier := LoyaltyTier{
			SourceID: group.ID, Code: code, Name: truncateRunes(oneLine(group.Name), 60),
			DiscountBPS: percentBPS(group.ServerPercent), TrafficDiscountBPS: percentBPS(group.TrafficPercent),
			DeviceDiscountBPS: percentBPS(group.DevicePercent), Priority: int32(group.Priority),
		}
		if utf8.RuneCountInString(tier.Name) == 0 {
			tier.Name = code
		}
		switch {
		case group.IsDefault:
			tier.Threshold = 0
		case group.AutoSpentKopeks != nil && *group.AutoSpentKopeks > 0:
			tier.Threshold = *group.AutoSpentKopeks
		default:
			// Given only by an operator in Bedolaga, so only by an operator here.
			tier.ManualOnly = true
		}
		if group.HasPeriodRules {
			decision.Warnings = append(decision.Warnings, CodePeriodDiscountsDropped)
		}
		if tier.DiscountBPS > 0 || tier.TrafficDiscountBPS > 0 || tier.DeviceDiscountBPS > 0 {
			discounted = true
		}
		decision.Tiers = append(decision.Tiers, tier)
	}
	sort.SliceStable(decision.Tiers, func(i, j int) bool { return decision.Tiers[i].Threshold < decision.Tiers[j].Threshold })
	for index := range decision.Tiers {
		decision.Tiers[index].SortOrder = int32(index)
	}

	carried := map[string]bool{}
	switch {
	case len(groups) == 0:
		decision.Action = ActionSkip
	case !discounted:
		decision.Action, decision.Codes = ActionSkip, []string{CodeNoDiscountGroups}
	case target.LoyaltyProgramCount > 0:
		// A programme carried before has exactly these tiers; one an operator
		// built is theirs. Either way memberships go only into tiers it has.
		decision.Action = ActionUnchanged
		for _, tier := range decision.Tiers {
			if !target.LoyaltyTierCodes[tier.Code] {
				decision.Action, decision.Codes = ActionSkip, []string{CodeLoyaltyProgramExists}
				break
			}
		}
		for code := range target.LoyaltyTierCodes {
			carried[code] = true
		}
	default:
		decision.Action = ActionCreate
		for _, tier := range decision.Tiers {
			carried[tier.Code] = true
		}
	}
	if len(groups) > 0 {
		plan.Loyalty = &decision
	}

	defaults := map[int64]bool{}
	for _, group := range groups {
		defaults[group.ID] = group.IsDefault
	}
	members := append([]PromoMember(nil), source.PromoMembers...)
	sort.Slice(members, func(i, j int) bool {
		if members[i].UserID != members[j].UserID {
			return members[i].UserID < members[j].UserID
		}
		return members[i].GroupID < members[j].GroupID
	})
	for _, member := range members {
		membership := MembershipDecision{Source: member, TierCode: codes[member.GroupID]}
		membership.Note = "Bedolaga"
		if by := strings.TrimSpace(member.AssignedBy); by != "" {
			membership.Note = "Bedolaga: " + by
		}
		index, ok := userIndex[member.UserID]
		if ok {
			membership.CustomerID = plan.Users[index].CustomerID
		}
		switch {
		case !ok || !plan.Users[index].Importable():
			membership.Action, membership.Codes = ActionSkip, []string{CodeUserNotImported}
		case defaults[member.GroupID]:
			// Everybody is in the default group; it is the bottom tier.
			membership.Action, membership.Codes = ActionSkip, []string{CodeDefaultGroup}
		case membership.TierCode == "" || !carried[membership.TierCode]:
			membership.Action, membership.Codes = ActionSkip, []string{CodeTierNotCarried}
		case membership.CustomerID != "" && target.LoyaltyMembers[membership.CustomerID+":"+membership.TierCode]:
			membership.Action = ActionUnchanged
		default:
			membership.Action = ActionCreate
		}
		plan.Memberships = append(plan.Memberships, membership)
	}
}
