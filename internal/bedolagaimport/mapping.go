package bedolagaimport

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Mapping is the operator's half of the migration: the decisions the source
// data cannot make for itself. It is read from a YAML or JSON file.
//
//	currency: RUB
//	blockedUsers: suspend
//	walletReason: "Balance carried over from the previous bot"
//	plans:
//	  - tariffId: 3
//	    planVersionId: 0b7c…
//	  - tariffId: 3
//	    periodDays: 360
//	    planVersionId: 5d1e…
//	  - trial: true
//	    planVersionId: 9a40…
//	  - noTariff: true
//	    planVersionId: 77f2…
type Mapping struct {
	// Currency is the ISO code Bedolaga's kopek amounts are carried into.
	// Bedolaga stores every balance in kopeks, which are RUB minor units.
	Currency string `yaml:"currency"`
	// BlockedUsers decides what a Bedolaga `blocked` user becomes. Bedolaga
	// writes that status both when an operator bans someone and when the
	// broadcast notices that a customer blocked the bot, so the source cannot
	// tell the two apart and the operator has to choose: `suspend` or `active`.
	BlockedUsers string `yaml:"blockedUsers"`
	// WalletReason is shown to the customer beside the carried-over balance.
	WalletReason string `yaml:"walletReason"`
	// Plans maps Bedolaga tariffs to Omniflow plan versions.
	Plans []PlanRule `yaml:"plans"`
}

// PlanRule maps one kind of Bedolaga subscription to an Omniflow plan version.
//
// A subscription is matched by its tariff, or — when Bedolaga sold without
// tariffs — by NoTariff. A rule with PeriodDays only matches a subscription
// whose start-to-end length rounds to that many days, and wins over a rule for
// the same tariff without one. A Trial rule matches only trial subscriptions,
// and a trial subscription only falls back to a non-trial rule when no trial
// rule matches it.
type PlanRule struct {
	TariffID      *int64 `yaml:"tariffId"`
	NoTariff      bool   `yaml:"noTariff"`
	PeriodDays    *int   `yaml:"periodDays"`
	Trial         bool   `yaml:"trial"`
	PlanVersionID string `yaml:"planVersionId"`
}

// Blocked-user policies.
const (
	BlockedSuspend = "suspend"
	BlockedActive  = "active"
)

// DefaultWalletReason is used when the mapping does not name one. Customers
// read it in their wallet history.
const DefaultWalletReason = "Balance carried over from the previous bot"

var (
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
	uuidPattern     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// ParseMapping reads and validates a mapping file. Unknown keys are refused:
// a misspelt `planVersionID` silently ignored would leave a tariff unmapped
// and its customers without their subscription.
func ParseMapping(reader io.Reader) (Mapping, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, 1<<20))
	if err != nil {
		return Mapping{}, err
	}
	var mapping Mapping
	if len(bytes.TrimSpace(raw)) > 0 {
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		decoder.KnownFields(true)
		if err := decoder.Decode(&mapping); err != nil {
			return Mapping{}, fmt.Errorf("mapping file: %w", err)
		}
	}
	if err := mapping.normalize(); err != nil {
		return Mapping{}, err
	}
	return mapping, nil
}

func (mapping *Mapping) normalize() error {
	mapping.Currency = strings.ToUpper(strings.TrimSpace(mapping.Currency))
	if mapping.Currency == "" {
		mapping.Currency = "RUB"
	}
	if !currencyPattern.MatchString(mapping.Currency) {
		return errors.New("mapping file: currency must be a three-letter ISO code")
	}
	mapping.BlockedUsers = strings.ToLower(strings.TrimSpace(mapping.BlockedUsers))
	if mapping.BlockedUsers != "" && mapping.BlockedUsers != BlockedSuspend && mapping.BlockedUsers != BlockedActive {
		return errors.New("mapping file: blockedUsers must be \"suspend\" or \"active\"")
	}
	mapping.WalletReason = strings.TrimSpace(mapping.WalletReason)
	if mapping.WalletReason == "" {
		mapping.WalletReason = DefaultWalletReason
	}
	if len([]rune(mapping.WalletReason)) > 200 {
		return errors.New("mapping file: walletReason must be at most 200 characters")
	}
	seen := map[string]int{}
	for index := range mapping.Plans {
		rule := &mapping.Plans[index]
		rule.PlanVersionID = strings.ToLower(strings.TrimSpace(rule.PlanVersionID))
		position := index + 1
		switch {
		case !uuidPattern.MatchString(rule.PlanVersionID):
			return fmt.Errorf("mapping file: plans[%d]: planVersionId must be a UUID", position)
		case rule.TariffID != nil && rule.NoTariff:
			return fmt.Errorf("mapping file: plans[%d]: tariffId and noTariff are exclusive", position)
		case rule.TariffID == nil && !rule.NoTariff && !rule.Trial:
			return fmt.Errorf("mapping file: plans[%d]: name a tariffId, noTariff, or trial", position)
		case rule.TariffID != nil && *rule.TariffID <= 0:
			return fmt.Errorf("mapping file: plans[%d]: tariffId must be positive", position)
		case rule.PeriodDays != nil && *rule.PeriodDays <= 0:
			return fmt.Errorf("mapping file: plans[%d]: periodDays must be positive", position)
		}
		key := rule.selectorKey()
		if previous, ok := seen[key]; ok {
			return fmt.Errorf("mapping file: plans[%d] repeats the selector of plans[%d]", position, previous)
		}
		seen[key] = position
	}
	return nil
}

func (rule PlanRule) selectorKey() string {
	tariff := "any"
	switch {
	case rule.TariffID != nil:
		tariff = fmt.Sprintf("tariff:%d", *rule.TariffID)
	case rule.NoTariff:
		tariff = "none"
	}
	period := "any"
	if rule.PeriodDays != nil {
		period = fmt.Sprintf("%d", *rule.PeriodDays)
	}
	return fmt.Sprintf("%s|%s|%t", tariff, period, rule.Trial)
}

func (rule PlanRule) matchesTariff(tariffID *int64) bool {
	switch {
	case rule.TariffID != nil:
		return tariffID != nil && *tariffID == *rule.TariffID
	case rule.NoTariff:
		return tariffID == nil
	default:
		// A trial rule without a tariff selector matches any tariff.
		return rule.Trial
	}
}

// Resolve returns the plan version a subscription maps to, or "" when no rule
// covers it. More specific rules win: a tariff-and-period rule over a tariff
// rule, a tariff rule over a tariff-less trial rule.
func (mapping Mapping) Resolve(subscription Subscription) string {
	days := periodDays(subscription.StartDate, subscription.EndDate)
	best, bestScore := "", -1
	consider := func(trialPass bool) {
		for _, rule := range mapping.Plans {
			if rule.Trial != trialPass || !rule.matchesTariff(subscription.TariffID) {
				continue
			}
			if rule.PeriodDays != nil && *rule.PeriodDays != days {
				continue
			}
			score := 0
			if rule.TariffID != nil || rule.NoTariff {
				score += 2
			}
			if rule.PeriodDays != nil {
				score++
			}
			if score > bestScore {
				best, bestScore = rule.PlanVersionID, score
			}
		}
	}
	if subscription.IsTrial {
		consider(true)
		if best != "" {
			return best
		}
	}
	consider(false)
	return best
}

// PlanVersionIDs lists every plan version the mapping names, in file order.
func (mapping Mapping) PlanVersionIDs() []string {
	ids := make([]string, 0, len(mapping.Plans))
	for _, rule := range mapping.Plans {
		ids = append(ids, rule.PlanVersionID)
	}
	return ids
}

func periodDays(start, end time.Time) int {
	if start.IsZero() || !end.After(start) {
		return 0
	}
	return int((end.Sub(start) + 12*time.Hour) / (24 * time.Hour))
}
