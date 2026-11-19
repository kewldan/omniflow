package bedolagaimport

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// Section counts the decisions of one record kind.
type Section struct {
	Actions  map[Action]int `json:"actions"`
	Codes    map[string]int `json:"codes"`
	Warnings map[string]int `json:"warnings"`
}

func newSection() Section {
	return Section{Actions: map[Action]int{}, Codes: map[string]int{}, Warnings: map[string]int{}}
}

func (section *Section) add(action Action, codes, warnings []string) {
	section.Actions[action]++
	for _, code := range codes {
		section.Codes[code]++
	}
	for _, warning := range warnings {
		section.Warnings[warning]++
	}
}

// SourceTotals are the reconciliation figures of the Bedolaga side.
type SourceTotals struct {
	Users                int   `json:"users"`
	DeletedUsers         int   `json:"deletedUsers"`
	BlockedUsers         int   `json:"blockedUsers"`
	BalanceTotalMinor    int64 `json:"balanceTotalMinor"`
	NegativeBalanceUsers int   `json:"negativeBalanceUsers"`
	Subscriptions        int   `json:"subscriptions"`
	ActiveSubscriptions  int   `json:"activeSubscriptions"`
	ReferralPairs        int   `json:"referralPairs"`
}

// BalanceSection adds the money totals to the balance decisions.
type BalanceSection struct {
	Section
	CreditMinor   int64 `json:"creditMinor"`
	DebitMinor    int64 `json:"debitMinor"`
	SourceMinor   int64 `json:"sourceMinor"`
	ImportedMinor int64 `json:"importedMinor"`
}

// SubscriptionSection adds how many subscriptions carry an entitlement.
type SubscriptionSection struct {
	Section
	WithEntitlement int `json:"withEntitlement"`
	LinkOnly        int `json:"linkOnly"`
}

// UnmappedTariff is a tariff with active subscriptions and no plan version.
type UnmappedTariff struct {
	TariffID      *int64 `json:"tariffId"`
	Name          string `json:"name"`
	Trial         bool   `json:"trial"`
	PeriodDays    int    `json:"periodDays"`
	Subscriptions int    `json:"subscriptions"`
}

// PlanVersionUse is how many subscriptions land on one plan version.
type PlanVersionUse struct {
	ID            string `json:"id"`
	PlanCode      string `json:"planCode"`
	PlanKind      string `json:"planKind"`
	Subscriptions int    `json:"subscriptions"`
}

// Problem names one record that needs an operator, by its Bedolaga row ID.
// No Telegram ID, name, or contact is ever written into a report.
type Problem struct {
	Kind     string   `json:"kind"`
	SourceID int64    `json:"sourceId"`
	Action   Action   `json:"action"`
	Codes    []string `json:"codes"`
}

// Report summarises a plan. It is printed by `plan`, stored with every
// `apply` run, and is safe to share: it carries counts, Bedolaga row IDs,
// and tariff names only.
type Report struct {
	GeneratedAt     time.Time           `json:"generatedAt"`
	Currency        string              `json:"currency"`
	Source          SourceTotals        `json:"source"`
	Users           Section             `json:"users"`
	Balances        BalanceSection      `json:"balances"`
	Subscriptions   SubscriptionSection `json:"subscriptions"`
	Referrals       Section             `json:"referrals"`
	UnmappedTariffs []UnmappedTariff    `json:"unmappedTariffs"`
	PlanVersions    []PlanVersionUse    `json:"planVersions"`
	// Available lists every Omniflow plan version a mapping may name.
	Available   []PlanVersion `json:"availablePlanVersions"`
	Problems    []Problem     `json:"problems"`
	Archive     Archive       `json:"archive"`
	NotMigrated []string      `json:"notMigrated"`
}

// NotMigrated lists what the importer deliberately leaves in Bedolaga.
var NotMigrated = []string{
	"payment history (transactions and per-provider payment tables): stays in the archived Bedolaga dump",
	"Telegram Stars shop orders (stars_orders): counted here, stays in the archived dump",
	"support tickets and their messages: close them in Bedolaga before cutover",
	"promo codes, promo groups and their discounts: recreate the ones you still want in the panel",
	"referral earnings history and partner withdrawal requests: the earned money is already in the balance",
	"saved payment methods and recurring payment agreements: customers pay again once in Omniflow",
	"OAuth links (Google, Yandex, Discord, VK) and cabinet passwords: customers sign in with Telegram",
	"Bedolaga referral codes and links: every customer gets a new Omniflow referral code",
}

// Pending reports how many decisions a run would still write.
func (plan Plan) Pending() int {
	count := 0
	for _, decision := range plan.Users {
		if decision.Action == ActionCreate {
			count++
		}
	}
	for _, decision := range plan.Balances {
		if decision.Action == ActionCreate || decision.Action == ActionRefresh {
			count++
		}
	}
	for _, decision := range plan.Subscriptions {
		if decision.Action == ActionCreate || decision.Action == ActionRefresh {
			count++
		}
	}
	for _, decision := range plan.Referrals {
		if decision.Action == ActionCreate {
			count++
		}
	}
	return count
}

func buildReport(plan Plan, source Source, tariffs map[int64]Tariff, mapping Mapping, now time.Time) Report {
	report := Report{
		GeneratedAt: now, Currency: mapping.Currency, Users: newSection(), Referrals: newSection(),
		Balances:        BalanceSection{Section: newSection()},
		Subscriptions:   SubscriptionSection{Section: newSection()},
		UnmappedTariffs: []UnmappedTariff{}, PlanVersions: []PlanVersionUse{}, Problems: []Problem{},
		Archive: source.Archive, NotMigrated: NotMigrated,
	}
	for _, user := range source.Users {
		report.Source.Users++
		switch user.Status {
		case SourceStatusDeleted:
			report.Source.DeletedUsers++
			continue
		case SourceStatusBlocked:
			report.Source.BlockedUsers++
		}
		if user.BalanceKopeks < 0 {
			report.Source.NegativeBalanceUsers++
		} else {
			report.Source.BalanceTotalMinor += user.BalanceKopeks
		}
		if user.ReferredByID != nil {
			report.Source.ReferralPairs++
		}
	}
	for _, decision := range plan.Users {
		report.Users.add(decision.Action, decision.Codes, decision.Warnings)
		if decision.Action == ActionConflict {
			report.Problems = append(report.Problems, Problem{Kind: KindUser, SourceID: decision.Source.ID, Action: decision.Action, Codes: decision.Codes})
		}
	}
	for _, decision := range plan.Balances {
		report.Balances.add(decision.Action, decision.Codes, decision.Warnings)
		if decision.Action == ActionConflict {
			report.Problems = append(report.Problems, Problem{Kind: "balance", SourceID: decision.SourceUserID, Action: decision.Action, Codes: decision.Codes})
			continue
		}
		report.Balances.SourceMinor += decision.SourceMinor
		report.Balances.ImportedMinor += decision.ImportedMinor
		if decision.Action == ActionCreate || decision.Action == ActionRefresh {
			if decision.DeltaMinor > 0 {
				report.Balances.CreditMinor += decision.DeltaMinor
			} else {
				report.Balances.DebitMinor += -decision.DeltaMinor
			}
		}
	}
	unmapped := map[string]*UnmappedTariff{}
	versions := map[string]*PlanVersionUse{}
	for _, decision := range plan.Subscriptions {
		report.Source.Subscriptions++
		if decision.Active {
			report.Source.ActiveSubscriptions++
		}
		report.Subscriptions.add(decision.Action, decision.Codes, decision.Warnings)
		switch decision.Action {
		case ActionConflict:
			report.Problems = append(report.Problems, Problem{Kind: KindSubscription, SourceID: decision.Source.ID, Action: decision.Action, Codes: decision.Codes})
		case ActionCreate, ActionRefresh, ActionUnchanged:
			if decision.Active {
				report.Subscriptions.WithEntitlement++
			} else {
				report.Subscriptions.LinkOnly++
			}
		}
		if decision.PlanVersion.ID != "" && decision.Action != ActionConflict && decision.Action != ActionSkip {
			use := versions[decision.PlanVersion.ID]
			if use == nil {
				use = &PlanVersionUse{ID: decision.PlanVersion.ID, PlanCode: decision.PlanVersion.PlanCode, PlanKind: decision.PlanVersion.PlanKind}
				versions[decision.PlanVersion.ID] = use
			}
			use.Subscriptions++
		}
		if hasCode(decision.Codes, CodeUnmappedTariff) {
			days := periodDays(decision.Source.StartDate, decision.Source.EndDate)
			key := fmt.Sprintf("%v|%t|%d", optionalInt64(decision.Source.TariffID), decision.Source.IsTrial, days)
			entry := unmapped[key]
			if entry == nil {
				entry = &UnmappedTariff{TariffID: decision.Source.TariffID, Trial: decision.Source.IsTrial, PeriodDays: days}
				if decision.Source.TariffID != nil {
					entry.Name = tariffs[*decision.Source.TariffID].Name
				}
				unmapped[key] = entry
			}
			entry.Subscriptions++
		}
	}
	for _, decision := range plan.Referrals {
		report.Referrals.add(decision.Action, decision.Codes, nil)
		if decision.Action == ActionConflict {
			report.Problems = append(report.Problems, Problem{Kind: KindReferral, SourceID: decision.RefereeID, Action: decision.Action, Codes: decision.Codes})
		}
	}
	for _, entry := range unmapped {
		report.UnmappedTariffs = append(report.UnmappedTariffs, *entry)
	}
	sort.Slice(report.UnmappedTariffs, func(i, j int) bool {
		left, right := report.UnmappedTariffs[i], report.UnmappedTariffs[j]
		if optionalInt64(left.TariffID) != optionalInt64(right.TariffID) {
			return optionalInt64(left.TariffID) < optionalInt64(right.TariffID)
		}
		if left.Trial != right.Trial {
			return !left.Trial
		}
		return left.PeriodDays < right.PeriodDays
	})
	for _, use := range versions {
		report.PlanVersions = append(report.PlanVersions, *use)
	}
	sort.Slice(report.PlanVersions, func(i, j int) bool { return report.PlanVersions[i].ID < report.PlanVersions[j].ID })
	return report
}

func hasCode(codes []string, code string) bool {
	for _, candidate := range codes {
		if candidate == code {
			return true
		}
	}
	return false
}

// WriteText renders the report for a terminal.
func (report Report) WriteText(writer io.Writer) {
	fmt.Fprintf(writer, "Bedolaga → Omniflow plan (%s)\n\n", report.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(writer, "Source: %d users (%d deleted, %d blocked), %d subscriptions (%d active), %d referral pairs\n",
		report.Source.Users, report.Source.DeletedUsers, report.Source.BlockedUsers,
		report.Source.Subscriptions, report.Source.ActiveSubscriptions, report.Source.ReferralPairs)
	fmt.Fprintf(writer, "        balances %s %s across non-negative wallets, %d negative\n\n",
		FormatMinor(report.Source.BalanceTotalMinor), report.Currency, report.Source.NegativeBalanceUsers)
	writeSection(writer, "Users", report.Users)
	writeSection(writer, "Balances", report.Balances.Section)
	fmt.Fprintf(writer, "  to credit %s %s, to debit %s %s (source %s, already moved %s)\n",
		FormatMinor(report.Balances.CreditMinor), report.Currency, FormatMinor(report.Balances.DebitMinor), report.Currency,
		FormatMinor(report.Balances.SourceMinor), FormatMinor(report.Balances.ImportedMinor))
	writeSection(writer, "Subscriptions", report.Subscriptions.Section)
	fmt.Fprintf(writer, "  %d with an entitlement, %d linked to their Remnawave user only\n",
		report.Subscriptions.WithEntitlement, report.Subscriptions.LinkOnly)
	writeSection(writer, "Referrals", report.Referrals)
	if len(report.UnmappedTariffs) > 0 {
		fmt.Fprintln(writer, "\nUnmapped tariffs (add them to the mapping file):")
		for _, entry := range report.UnmappedTariffs {
			tariff := "no tariff"
			if entry.TariffID != nil {
				tariff = fmt.Sprintf("tariff %d %q", *entry.TariffID, entry.Name)
			}
			kind := ""
			if entry.Trial {
				kind = " trial"
			}
			fmt.Fprintf(writer, "  %s%s, %d days: %d subscriptions\n", tariff, kind, entry.PeriodDays, entry.Subscriptions)
		}
	}
	if len(report.PlanVersions) > 0 {
		fmt.Fprintln(writer, "\nPlan versions used:")
		for _, use := range report.PlanVersions {
			fmt.Fprintf(writer, "  %s (%s, %s): %d subscriptions\n", use.ID, use.PlanCode, use.PlanKind, use.Subscriptions)
		}
	}
	fmt.Fprintln(writer, "\nLeft in the Bedolaga archive:")
	writeBuckets(writer, "transactions by type", report.Archive.Transactions)
	writeBuckets(writer, "completed deposits by method", report.Archive.CompletedDeposits)
	writeBuckets(writer, "Stars shop orders by status", report.Archive.StarsOrders)
	writeBuckets(writer, "tickets by status", report.Archive.Tickets)
	writeBuckets(writer, "active promo codes by type", report.Archive.ActivePromoCodes)
	writeBuckets(writer, "promo groups (members)", report.Archive.PromoGroups)
	writeBuckets(writer, "withdrawal requests by status", report.Archive.WithdrawalRequests)
	fmt.Fprintf(writer, "  referral earnings: %d records, %s\n", report.Archive.ReferralEarnings.Count, FormatMinor(report.Archive.ReferralEarnings.SumMinor))
	fmt.Fprintf(writer, "  users with an OAuth link: %d\n", report.Archive.OAuthLinkedUsers)
	if len(report.Archive.MissingTables) > 0 {
		fmt.Fprintf(writer, "  tables not present in this database: %s\n", strings.Join(report.Archive.MissingTables, ", "))
	}
	if len(report.Available) > 0 {
		fmt.Fprintln(writer, "\nOmniflow plan versions a mapping can name:")
		for _, version := range report.Available {
			retired := ""
			if version.Retired {
				retired = ", retired"
			}
			fmt.Fprintf(writer, "  %s  %s (%s%s)\n", version.ID, version.PlanCode, version.PlanKind, retired)
		}
	}
	if len(report.Problems) > 0 {
		fmt.Fprintf(writer, "\n%d records need attention; their Bedolaga IDs are in the JSON report.\n", len(report.Problems))
	}
}

func writeSection(writer io.Writer, title string, section Section) {
	fmt.Fprintf(writer, "%-14s", title+":")
	for _, action := range []Action{ActionCreate, ActionRefresh, ActionUnchanged, ActionConflict, ActionSkip} {
		fmt.Fprintf(writer, " %s %d", action, section.Actions[action])
	}
	fmt.Fprintln(writer)
	writeCounts(writer, "conflicts and skips", section.Codes)
	writeCounts(writer, "warnings", section.Warnings)
}

func writeCounts(writer io.Writer, label string, counts map[string]int) {
	if len(counts) == 0 {
		return
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	fmt.Fprintf(writer, "  %s: %s\n", label, strings.Join(parts, " "))
}

func writeBuckets(writer io.Writer, label string, buckets []CountSum) {
	if len(buckets) == 0 {
		fmt.Fprintf(writer, "  %s: none\n", label)
		return
	}
	parts := make([]string, 0, len(buckets))
	for _, bucket := range buckets {
		if bucket.SumMinor != 0 {
			parts = append(parts, fmt.Sprintf("%s=%d (%s)", bucket.Key, bucket.Count, FormatMinor(bucket.SumMinor)))
		} else {
			parts = append(parts, fmt.Sprintf("%s=%d", bucket.Key, bucket.Count))
		}
	}
	fmt.Fprintf(writer, "  %s: %s\n", label, strings.Join(parts, " "))
}

// FormatMinor renders an amount of two-decimal minor units.
func FormatMinor(amount int64) string {
	sign := ""
	if amount < 0 {
		sign, amount = "-", -amount
	}
	return fmt.Sprintf("%s%d.%02d", sign, amount/100, amount%100)
}
