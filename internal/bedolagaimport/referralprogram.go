package bedolagaimport

import (
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// ReferralTerms is what Bedolaga's referral programme paid, as the bot itself
// resolved it: its compiled-in defaults, under the `REFERRAL_*` overrides an
// operator saved in `system_settings`, under the ones its `.env` set — a key
// set in `.env` is one the admin panel can no longer change. Only the database
// is read, so the `.env` half comes from the mapping's `referralSettings`.
type ReferralTerms struct {
	// Read is false when the Bedolaga database has no `system_settings`
	// table; nothing is decided then.
	Read     bool
	Settings map[string]string
	Levels   []ReferralLevel
}

// ReferralLevel is one row of Bedolaga's `referral_reward_levels`: what one
// link of the referral chain pays under the "levels" scheme.
type ReferralLevel struct {
	Level               int64
	Active              bool
	Mode                string
	Trigger             string
	ReferrerPercent     *int64
	ReferrerFixedKopeks *int64
	ReferrerDays        int64
	RefereeFixedKopeks  *int64
	RefereeDays         int64
	MaxPayments         int64
	RequiredReferrals   int64
	TariffReward        bool
}

// ReferralProgramTerms is an Omniflow referral programme, in the fields of
// `referral_programs`.
type ReferralProgramTerms struct {
	Enabled                   bool                 `json:"enabled"`
	InviterRewardMinor        int64                `json:"inviterRewardMinor"`
	InviteeRewardMinor        int64                `json:"inviteeRewardMinor"`
	InviterRewardDays         int32                `json:"inviterRewardDays"`
	InviteeRewardDays         int32                `json:"inviteeRewardDays"`
	CommissionBPS             int32                `json:"commissionBps"`
	FirstPaymentCommissionBPS *int32               `json:"firstPaymentCommissionBps,omitempty"`
	CommissionPaymentCap      *int32               `json:"commissionPaymentCap,omitempty"`
	CommissionTriggers        []string             `json:"commissionTriggers"`
	CommissionTiers           []ReferralCommission `json:"commissionTiers"`
	AllowRewardChoice         bool                 `json:"allowRewardChoice"`
}

// ReferralCommission is one rung of the commission ladder: the rate once the
// inviter has that many qualified referrals.
type ReferralCommission struct {
	Referrals int32 `json:"referrals"`
	BPS       int32 `json:"bps"`
}

// Pays reports whether the terms reward anybody at all.
func (terms ReferralProgramTerms) Pays() bool {
	if terms.InviterRewardMinor > 0 || terms.InviteeRewardMinor > 0 ||
		terms.InviterRewardDays > 0 || terms.InviteeRewardDays > 0 || terms.CommissionBPS > 0 {
		return true
	}
	if terms.FirstPaymentCommissionBPS != nil && *terms.FirstPaymentCommissionBPS > 0 {
		return true
	}
	for _, tier := range terms.CommissionTiers {
		if tier.BPS > 0 {
			return true
		}
	}
	return false
}

// Same reports whether two programmes pay the same.
func (terms ReferralProgramTerms) Same(other ReferralProgramTerms) bool {
	equalPointer := func(left, right *int32) bool {
		return left == nil && right == nil || left != nil && right != nil && *left == *right
	}
	triggers := func(values []string) []string {
		sorted := slices.Clone(values)
		slices.Sort(sorted)
		return sorted
	}
	return terms.Enabled == other.Enabled &&
		terms.InviterRewardMinor == other.InviterRewardMinor && terms.InviteeRewardMinor == other.InviteeRewardMinor &&
		terms.InviterRewardDays == other.InviterRewardDays && terms.InviteeRewardDays == other.InviteeRewardDays &&
		terms.CommissionBPS == other.CommissionBPS &&
		equalPointer(terms.FirstPaymentCommissionBPS, other.FirstPaymentCommissionBPS) &&
		equalPointer(terms.CommissionPaymentCap, other.CommissionPaymentCap) &&
		slices.Equal(triggers(terms.CommissionTriggers), triggers(other.CommissionTriggers)) &&
		slices.Equal(terms.CommissionTiers, other.CommissionTiers) &&
		terms.AllowRewardChoice == other.AllowRewardChoice
}

// ReferralProgramDecision carries Bedolaga's referral terms into Omniflow's
// programme. It is made only while Omniflow has no programme, or one that
// already pays exactly this.
type ReferralProgramDecision struct {
	Action   Action               `json:"action"`
	Codes    []string             `json:"codes,omitempty"`
	Warnings []string             `json:"warnings,omitempty"`
	Scheme   string               `json:"scheme"`
	Terms    ReferralProgramTerms `json:"terms"`
}

// Codes of the referral programme decision.
const (
	// CodeReferralProgramExists: Omniflow already has a programme that pays
	// something else, which an operator configured; it stands.
	CodeReferralProgramExists = "referral_program_exists"
	// CodeReferralProgramPaysNothing: Bedolaga's terms reward nobody, so
	// there is nothing to switch on.
	CodeReferralProgramPaysNothing = "referral_program_pays_nothing"

	// Warnings: what Omniflow's programme cannot say the way Bedolaga did.
	WarnReferralDisabled          = "referral_program_disabled"
	WarnReferralFirstPaidOrder    = "referral_qualifies_on_first_paid_order"
	WarnReferralMinimumTopup      = "referral_minimum_topup_not_carried"
	WarnReferralDeeperLevels      = "referral_deeper_levels_not_carried"
	WarnReferralRequiredReferrals = "referral_required_referrals_not_carried"
	WarnReferralTariffReward      = "referral_tariff_reward_as_days"
	WarnReferralRecurringReward   = "referral_recurring_fixed_reward_dropped"
	WarnReferralDaysClamped       = "referral_days_clamped"
)

// referralDefaults are Bedolaga's compiled-in values (app/config.py).
var referralDefaults = map[string]string{
	"REFERRAL_PROGRAM_ENABLED":                  "true",
	"REFERRAL_REWARD_SCHEME":                    "legacy",
	"REFERRAL_MINIMUM_TOPUP_KOPEKS":             "10000",
	"REFERRAL_FIRST_TOPUP_BONUS_KOPEKS":         "10000",
	"REFERRAL_INVITER_BONUS_KOPEKS":             "10000",
	"REFERRAL_COMMISSION_PERCENT":               "25",
	"REFERRAL_FIRST_PAYMENT_COMMISSION_PERCENT": "",
	"REFERRAL_RECURRING_COMMISSION_TIERS":       "",
	"REFERRAL_MAX_COMMISSION_PAYMENTS":          "0",
	"REFERRAL_ALLOW_REWARD_KIND_CHOICE":         "false",
}

// maxReferralDays is the most days Omniflow's programme grants one side.
const maxReferralDays = 3650

type referralSettings map[string]string

func (settings referralSettings) text(key string) string {
	return strings.TrimSpace(settings[key])
}

func (settings referralSettings) number(key string) (int64, bool) {
	value, err := strconv.ParseInt(settings.text(key), 10, 64)
	return value, err == nil
}

func (settings referralSettings) flag(key string) bool {
	switch strings.ToLower(settings.text(key)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// resolveReferralSettings layers the three sources in Bedolaga's order.
func resolveReferralSettings(database, env map[string]string) referralSettings {
	resolved := referralSettings{}
	for _, layer := range []map[string]string{referralDefaults, database, env} {
		for key, value := range layer {
			resolved[strings.ToUpper(strings.TrimSpace(key))] = value
		}
	}
	return resolved
}

func clampBPS(percent int64) int32 {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	return int32(percent * 100)
}

// clampInt32 bounds a parsed 64-bit value to int32. It compares against
// math.MaxInt32 explicitly rather than through min, which is the bound check
// a static analyser recognises before the narrowing conversion.
func clampInt32(value int64) int32 {
	if value > math.MaxInt32 {
		return math.MaxInt32
	}
	if value < math.MinInt32 {
		return math.MinInt32
	}
	return int32(value)
}

func optionalBPS(settings referralSettings, key string) *int32 {
	if percent, ok := settings.number(key); ok {
		bps := clampBPS(percent)
		return &bps
	}
	return nil
}

func positiveCap(value int64) *int32 {
	if value <= 0 {
		return nil
	}
	capped := clampInt32(value)
	return &capped
}

func nonNegative(value *int64) int64 {
	if value == nil || *value < 0 {
		return 0
	}
	return *value
}

// commissionLadder reads "0:10,10:15,50:20": from that many paying referrals,
// that percent. A rung at zero is the base rate, which Omniflow keeps apart.
func commissionLadder(raw string) (base *int32, tiers []ReferralCommission) {
	for _, part := range strings.Split(raw, ",") {
		threshold, percent, found := strings.Cut(strings.TrimSpace(part), ":")
		if !found {
			continue
		}
		count, countErr := strconv.ParseInt(strings.TrimSpace(threshold), 10, 64)
		rate, rateErr := strconv.ParseInt(strings.TrimSpace(percent), 10, 64)
		if countErr != nil || rateErr != nil {
			continue
		}
		bps := clampBPS(rate)
		if count <= 0 {
			base = &bps
			continue
		}
		tiers = append(tiers, ReferralCommission{Referrals: clampInt32(count), BPS: bps})
	}
	sort.Slice(tiers, func(i, j int) bool { return tiers[i].Referrals < tiers[j].Referrals })
	return base, tiers
}

func addWarning(decision *ReferralProgramDecision, code string) {
	if !slices.Contains(decision.Warnings, code) {
		decision.Warnings = append(decision.Warnings, code)
	}
}

func clampDays(decision *ReferralProgramDecision, days int64) int32 {
	if days > maxReferralDays {
		addWarning(decision, WarnReferralDaysClamped)
		days = maxReferralDays
	}
	if days < 0 {
		days = 0
	}
	return int32(days)
}

// legacyReferralTerms is Bedolaga's original scheme: a bonus to each side on
// the friend's first top-up above a minimum, then a percent of every top-up.
func legacyReferralTerms(decision *ReferralProgramDecision, settings referralSettings) {
	terms := &decision.Terms
	terms.InviterRewardMinor, _ = settings.number("REFERRAL_INVITER_BONUS_KOPEKS")
	terms.InviteeRewardMinor, _ = settings.number("REFERRAL_FIRST_TOPUP_BONUS_KOPEKS")
	terms.InviterRewardMinor, terms.InviteeRewardMinor = max(terms.InviterRewardMinor, 0), max(terms.InviteeRewardMinor, 0)
	percent, _ := settings.number("REFERRAL_COMMISSION_PERCENT")
	terms.CommissionBPS = clampBPS(percent)
	terms.FirstPaymentCommissionBPS = optionalBPS(settings, "REFERRAL_FIRST_PAYMENT_COMMISSION_PERCENT")
	if base, tiers := commissionLadder(settings.text("REFERRAL_RECURRING_COMMISSION_TIERS")); base != nil || tiers != nil {
		if base != nil {
			terms.CommissionBPS = *base
		}
		terms.CommissionTiers = tiers
	}
	payments, _ := settings.number("REFERRAL_MAX_COMMISSION_PAYMENTS")
	terms.CommissionPaymentCap = positiveCap(payments)
	if minimum, _ := settings.number("REFERRAL_MINIMUM_TOPUP_KOPEKS"); minimum > 0 &&
		(terms.InviterRewardMinor > 0 || terms.InviteeRewardMinor > 0) {
		addWarning(decision, WarnReferralMinimumTopup)
	}
	if terms.InviterRewardMinor > 0 || terms.InviteeRewardMinor > 0 {
		addWarning(decision, WarnReferralFirstPaidOrder)
	}
}

// levelReferralTerms is Bedolaga's "levels" scheme. Omniflow's programme is
// one link deep, so level 1 is carried and deeper levels are reported.
func levelReferralTerms(decision *ReferralProgramDecision, levels []ReferralLevel) {
	var first *ReferralLevel
	for index := range levels {
		level := &levels[index]
		if !level.Active {
			continue
		}
		if level.Level == 1 {
			first = level
		} else if level.Level > 1 {
			addWarning(decision, WarnReferralDeeperLevels)
		}
	}
	if first == nil {
		return
	}
	terms := &decision.Terms
	money := first.Mode != "days"
	days := first.Mode == "days" || first.Mode == "both"
	if first.RequiredReferrals > 0 {
		addWarning(decision, WarnReferralRequiredReferrals)
	}
	if days && first.TariffReward {
		addWarning(decision, WarnReferralTariffReward)
	}
	switch first.Trigger {
	case "every_topup":
		// A percent of every top-up is commission; a fixed sum or days on
		// every top-up has no counterpart.
		if money && first.ReferrerPercent != nil {
			terms.CommissionBPS = clampBPS(*first.ReferrerPercent)
		}
		if money && (nonNegative(first.ReferrerFixedKopeks) > 0 || nonNegative(first.RefereeFixedKopeks) > 0) ||
			days && (first.ReferrerDays > 0 || first.RefereeDays > 0) {
			addWarning(decision, WarnReferralRecurringReward)
		}
		terms.CommissionPaymentCap = positiveCap(first.MaxPayments)
		return
	default:
		// first_topup and registration pay once; Omniflow pays once on the
		// friend's first paid subscription order.
		if money {
			terms.InviterRewardMinor = nonNegative(first.ReferrerFixedKopeks)
			terms.InviteeRewardMinor = nonNegative(first.RefereeFixedKopeks)
			if first.ReferrerPercent != nil && *first.ReferrerPercent > 0 {
				bps := clampBPS(*first.ReferrerPercent)
				terms.FirstPaymentCommissionBPS = &bps
				one := int32(1)
				terms.CommissionPaymentCap = &one
			}
		}
		if days {
			terms.InviterRewardDays = clampDays(decision, first.ReferrerDays)
			terms.InviteeRewardDays = clampDays(decision, first.RefereeDays)
		}
		if terms.Pays() {
			addWarning(decision, WarnReferralFirstPaidOrder)
		}
	}
}

func decideReferralProgram(plan *Plan, source Source, target Target, mapping Mapping) {
	if !source.ReferralTerms.Read {
		return
	}
	settings := resolveReferralSettings(source.ReferralTerms.Settings, mapping.ReferralSettings)
	decision := ReferralProgramDecision{
		Scheme: strings.ToLower(settings.text("REFERRAL_REWARD_SCHEME")),
		Terms: ReferralProgramTerms{
			// Bedolaga's money arrives as top-ups and Omniflow's as orders too,
			// so both count; only what a provider settled ever earns.
			CommissionTriggers: []string{"subscription", "topup"},
			CommissionTiers:    []ReferralCommission{},
		},
	}
	if decision.Scheme == "levels" {
		levelReferralTerms(&decision, source.ReferralTerms.Levels)
		decision.Terms.AllowRewardChoice = settings.flag("REFERRAL_ALLOW_REWARD_KIND_CHOICE")
	} else {
		decision.Scheme = "legacy"
		legacyReferralTerms(&decision, settings)
	}
	if decision.Terms.CommissionTiers == nil {
		decision.Terms.CommissionTiers = []ReferralCommission{}
	}
	decision.Terms.Enabled = settings.flag("REFERRAL_PROGRAM_ENABLED")
	if !decision.Terms.Enabled {
		addWarning(&decision, WarnReferralDisabled)
	}
	switch {
	case !decision.Terms.Pays():
		decision.Action, decision.Codes = ActionSkip, []string{CodeReferralProgramPaysNothing}
		decision.Warnings = nil
	case target.ReferralProgram == nil:
		decision.Action = ActionCreate
	case target.ReferralProgram.Same(decision.Terms):
		decision.Action = ActionUnchanged
	default:
		decision.Action, decision.Codes = ActionSkip, []string{CodeReferralProgramExists}
	}
	plan.ReferralProgram = &decision
}
