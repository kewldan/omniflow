package bedolagaimport

import (
	"fmt"
	"io"
	"slices"
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
	// Payments are completed top-ups, whatever their method.
	Payments      int   `json:"payments"`
	PaymentsMinor int64 `json:"paymentsMinor"`
}

// TrialSection counts the trial claims by where they come from.
type TrialSection struct {
	Section
	// ByOrigin counts the claims to create: bedolaga_trial for a customer who
	// had a trial, bedolaga_history for one who subscribed without one.
	ByOrigin map[string]int `json:"byOrigin"`
}

// PeriodSection counts the subscription periods carried as history.
type PeriodSection struct {
	Section
	// Ended counts the periods already over, the customers the "expired"
	// segments find.
	Ended int `json:"ended"`
}

// GiftSection counts the paid gifts whose recipient has not received them.
// They are not carried; their Bedolaga IDs are in the problems.
type GiftSection struct {
	Unclaimed int            `json:"unclaimed"`
	SumMinor  int64          `json:"sumMinor"`
	ByStatus  map[string]int `json:"byStatus"`
}

// PaymentSection adds the money to the payment history decisions.
type PaymentSection struct {
	Section
	// CarriedMinor is what the carried payments add up to, created or already
	// there; Payers is how many customers they belong to.
	CarriedMinor int64 `json:"carriedMinor"`
	Payers       int   `json:"payers"`
	// ExcludedByMethod counts the deposits left out per method, so the
	// operator can see what excludedPaymentMethods removed.
	ExcludedByMethod map[string]int `json:"excludedByMethod"`
	// ByKind counts the carried payments by where Bedolaga recorded them:
	// subscription, gift_balance, gift.
	ByKind map[string]int `json:"byKind"`
	// StarsMinor is what the balance paid for Telegram Stars (stars_payment):
	// top-ups that went to Stars, which is why they are not VPN spend.
	StarsMinor int64 `json:"starsMinor"`
	StarsCount int64 `json:"starsCount"`
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
	GeneratedAt     time.Time                `json:"generatedAt"`
	Currency        string                   `json:"currency"`
	Source          SourceTotals             `json:"source"`
	Users           Section                  `json:"users"`
	Balances        BalanceSection           `json:"balances"`
	Subscriptions   SubscriptionSection      `json:"subscriptions"`
	Referrals       Section                  `json:"referrals"`
	Trials          TrialSection             `json:"trials"`
	Periods         PeriodSection            `json:"periods"`
	Payments        PaymentSection           `json:"payments"`
	Discounts       Section                  `json:"discounts"`
	ReferralCodes   Section                  `json:"referralCodes"`
	Gifts           GiftSection              `json:"gifts"`
	Campaigns       Section                  `json:"campaigns"`
	Attributions    Section                  `json:"campaignRegistrations"`
	Goods           GoodsSection             `json:"starsOrders"`
	Pages           Section                  `json:"pages"`
	Tickets         TicketSection            `json:"tickets"`
	Consents        Section                  `json:"consents"`
	Loyalty         Section                  `json:"loyalty"`
	Memberships     Section                  `json:"loyaltyMemberships"`
	ReferralProgram *ReferralProgramDecision `json:"referralProgram,omitempty"`
	History         []HistoryTable           `json:"history"`
	UnmappedTariffs []UnmappedTariff         `json:"unmappedTariffs"`
	PlanVersions    []PlanVersionUse         `json:"planVersions"`
	// Available lists every Omniflow plan version a mapping may name.
	Available   []PlanVersion `json:"availablePlanVersions"`
	Problems    []Problem     `json:"problems"`
	Archive     Archive       `json:"archive"`
	NotMigrated []string      `json:"notMigrated"`
}

// HistoryTable is what the archive does with one Bedolaga table.
type HistoryTable struct {
	Table   string `json:"table"`
	Create  int    `json:"create"`
	Already int    `json:"unchanged"`
	Skipped int    `json:"skip"`
}

// TicketSection is the support tickets carried as history.
type TicketSection struct {
	Section
	Messages int `json:"messages"`
}

// GoodsSection is the delivered Stars orders carried as shop history.
type GoodsSection struct {
	Section
	CarriedStars int64 `json:"carriedStars"`
	CarriedMinor int64 `json:"carriedMinor"`
	Buyers       int   `json:"buyers"`
}

// NotMigrated lists what the importer deliberately leaves in Bedolaga.
var NotMigrated = []string{
	"transactions other than completed top-ups (purchases from the balance, refunds, credits) and per-provider payment tables: stay in the archived dump; completed top-ups are carried as payment history",
	"Telegram Stars shop orders not delivered (paid, failed, refunded, in review): counted here, stay in the archived dump; delivered orders are carried as shop history",
	"advertising campaign bonuses already given: a carried registration is attributed with its date and is not given the bonus again",
	"gifts paid for and not yet received: counted here with their IDs; hand them over or refund them in Bedolaga before cutover",
	"files attached to support messages: the message says a file was attached; the file stays in the previous bot's chat",
	"promo codes: they become history; recreate a code you still want in the panel",
	"referral earnings history and partner withdrawal requests: the earned money is already in the balance",
	"saved payment methods and recurring payment agreements: customers pay again once in Omniflow",
	"cabinet passwords: customers sign in with Telegram, an e-mailed link, or a carried Google, Yandex, Discord, or VK ID link",
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
	for _, decision := range plan.Trials {
		if decision.Action == ActionCreate {
			count++
		}
	}
	for _, decision := range plan.Periods {
		if decision.Action == ActionCreate || decision.Action == ActionRefresh {
			count++
		}
	}
	for _, decision := range plan.Payments {
		if decision.Action == ActionCreate {
			count++
		}
	}
	for _, decision := range plan.Discounts {
		if decision.Action == ActionCreate || decision.Action == ActionRefresh {
			count++
		}
	}
	for _, decision := range plan.Aliases {
		if decision.Action == ActionCreate {
			count++
		}
	}
	for _, decision := range plan.Campaigns {
		if decision.Action == ActionCreate {
			count++
		}
	}
	for _, decision := range plan.Attributions {
		if decision.Action == ActionCreate {
			count++
		}
	}
	for _, decision := range plan.Goods {
		if decision.Action == ActionCreate {
			count++
		}
	}
	for _, decision := range plan.Pages {
		if decision.Action == ActionCreate {
			count++
		}
	}
	for _, decision := range plan.Tickets {
		if decision.Action == ActionCreate {
			count++
		}
	}
	for _, decision := range plan.Consents {
		if decision.Action == ActionCreate {
			count++
		}
	}
	if plan.Loyalty != nil && plan.Loyalty.Action == ActionCreate {
		count++
	}
	if plan.ReferralProgram != nil && plan.ReferralProgram.Action == ActionCreate {
		count++
	}
	for _, decision := range plan.Memberships {
		if decision.Action == ActionCreate {
			count++
		}
	}
	for _, decision := range plan.Archive {
		count += len(decision.Create)
	}
	return count
}

func buildReport(plan Plan, source Source, tariffs map[int64]Tariff, mapping Mapping, now time.Time) Report {
	report := Report{
		GeneratedAt: now, Currency: mapping.Currency, Users: newSection(), Referrals: newSection(),
		Balances:      BalanceSection{Section: newSection()},
		Subscriptions: SubscriptionSection{Section: newSection()},
		Trials:        TrialSection{Section: newSection(), ByOrigin: map[string]int{}},
		Periods:       PeriodSection{Section: newSection()},
		Payments:      PaymentSection{Section: newSection(), ExcludedByMethod: map[string]int{}, ByKind: map[string]int{}},
		Discounts:     newSection(),
		ReferralCodes: newSection(),
		Gifts:         GiftSection{ByStatus: map[string]int{}},
		Campaigns:     newSection(), Attributions: newSection(), Goods: GoodsSection{Section: newSection()},
		Pages: newSection(), Tickets: TicketSection{Section: newSection()}, Consents: newSection(),
		Loyalty: newSection(), Memberships: newSection(),
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
	for _, decision := range plan.Trials {
		report.Trials.add(decision.Action, decision.Codes, nil)
		switch decision.Action {
		case ActionCreate:
			report.Trials.ByOrigin[decision.Origin]++
		case ActionConflict:
			report.Problems = append(report.Problems, Problem{Kind: "trial", SourceID: decision.SourceUserID, Action: decision.Action, Codes: decision.Codes})
		}
	}
	for _, decision := range plan.Periods {
		report.Periods.add(decision.Action, decision.Codes, nil)
		if decision.Action != ActionSkip && !decision.EndsAt.After(now) {
			report.Periods.Ended++
		}
	}
	payers := map[int64]bool{}
	for _, payment := range source.Payments {
		report.Source.Payments++
		report.Source.PaymentsMinor += payment.AmountKopeks
	}
	for _, decision := range plan.Payments {
		report.Payments.add(decision.Action, decision.Codes, nil)
		switch decision.Action {
		case ActionCreate, ActionUnchanged:
			report.Payments.CarriedMinor += decision.Source.AmountKopeks
			report.Payments.ByKind[decision.Source.Kind]++
			payers[decision.Source.UserID] = true
		case ActionSkip:
			if hasCode(decision.Codes, CodePaymentMethodExcluded) {
				method := strings.ToLower(strings.TrimSpace(decision.Source.Method))
				if method == "" {
					method = "none"
				}
				report.Payments.ExcludedByMethod[method]++
			}
		}
	}
	report.Payments.Payers = len(payers)
	for _, bucket := range source.Archive.Transactions {
		if bucket.Key == "stars_payment" {
			report.Payments.StarsCount, report.Payments.StarsMinor = bucket.Count, bucket.SumMinor
		}
	}
	for _, decision := range plan.Discounts {
		report.Discounts.add(decision.Action, decision.Codes, nil)
	}
	for _, decision := range plan.Aliases {
		report.ReferralCodes.add(decision.Action, decision.Codes, nil)
		if decision.Action == ActionConflict {
			report.Problems = append(report.Problems, Problem{Kind: "referral_code", SourceID: decision.SourceUserID, Action: decision.Action, Codes: decision.Codes})
		}
	}
	for _, gift := range source.Gifts {
		report.Gifts.Unclaimed++
		report.Gifts.SumMinor += gift.AmountKopeks
		status := gift.Status
		if status == "" {
			status = "unknown"
		}
		report.Gifts.ByStatus[status]++
		report.Problems = append(report.Problems, Problem{Kind: "gift", SourceID: gift.ID, Action: ActionSkip, Codes: []string{CodeGiftUnclaimed}})
	}
	for _, decision := range plan.Campaigns {
		report.Campaigns.add(decision.Action, decision.Codes, decision.Warnings)
		if decision.Action == ActionConflict {
			report.Problems = append(report.Problems, Problem{Kind: "campaign", SourceID: decision.Source.ID, Action: decision.Action, Codes: decision.Codes})
		}
	}
	for _, decision := range plan.Attributions {
		report.Attributions.add(decision.Action, decision.Codes, nil)
	}
	buyers := map[int64]bool{}
	for _, decision := range plan.Goods {
		report.Goods.add(decision.Action, decision.Codes, nil)
		if decision.Action == ActionCreate || decision.Action == ActionUnchanged {
			report.Goods.CarriedStars += decision.Source.Quantity
			report.Goods.CarriedMinor += decision.Source.AmountKopeks
			buyers[decision.Source.UserID] = true
		}
	}
	report.Goods.Buyers = len(buyers)
	for _, decision := range plan.Pages {
		report.Pages.add(decision.Action, decision.Codes, decision.Warnings)
	}
	for _, decision := range plan.Tickets {
		report.Tickets.add(decision.Action, decision.Codes, decision.Warnings)
		if decision.Action == ActionCreate || decision.Action == ActionUnchanged {
			report.Tickets.Messages += len(decision.Source.Messages)
		}
	}
	for _, decision := range plan.Consents {
		report.Consents.add(decision.Action, decision.Codes, nil)
	}
	if plan.Loyalty != nil {
		report.Loyalty.add(plan.Loyalty.Action, plan.Loyalty.Codes, plan.Loyalty.Warnings)
	}
	for _, decision := range plan.Memberships {
		report.Memberships.add(decision.Action, decision.Codes, nil)
	}
	report.ReferralProgram = plan.ReferralProgram
	report.History = []HistoryTable{}
	for _, decision := range plan.Archive {
		report.History = append(report.History, HistoryTable{
			Table: decision.Table, Create: len(decision.Create), Already: decision.Already, Skipped: decision.Skipped,
		})
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
	writeSection(writer, "Trial claims", report.Trials.Section)
	writeCounts(writer, "to create by origin", report.Trials.ByOrigin)
	writeSection(writer, "Periods", report.Periods.Section)
	fmt.Fprintf(writer, "  %d ended, which the expired segments find\n", report.Periods.Ended)
	writeSection(writer, "Payments", report.Payments.Section)
	fmt.Fprintf(writer, "  carried %s %s from %d payers (source: %d top-ups, %s)\n",
		FormatMinor(report.Payments.CarriedMinor), report.Currency, report.Payments.Payers,
		report.Source.Payments, FormatMinor(report.Source.PaymentsMinor))
	writeCounts(writer, "carried by kind", report.Payments.ByKind)
	if report.Payments.StarsCount > 0 {
		fmt.Fprintf(writer, "  top-ups that went to Telegram Stars: %d purchases, %s %s — not VPN spend, not carried\n",
			report.Payments.StarsCount, FormatMinor(report.Payments.StarsMinor), report.Currency)
	}
	writeCounts(writer, "left out by method", report.Payments.ExcludedByMethod)
	writeSection(writer, "Discounts", report.Discounts)
	writeSection(writer, "Referral codes", report.ReferralCodes)
	writeSection(writer, "Campaigns", report.Campaigns)
	writeSection(writer, "Campaign regs", report.Attributions)
	writeSection(writer, "Stars orders", report.Goods.Section)
	fmt.Fprintf(writer, "  delivered orders carried: %d, %d stars, %s %s, from %d buyers\n",
		report.Goods.Actions[ActionCreate]+report.Goods.Actions[ActionUnchanged], report.Goods.CarriedStars,
		FormatMinor(report.Goods.CarriedMinor), report.Currency, report.Goods.Buyers)
	writeSection(writer, "Pages", report.Pages)
	writeSection(writer, "Tickets", report.Tickets.Section)
	fmt.Fprintf(writer, "  messages carried with them: %d\n", report.Tickets.Messages)
	writeSection(writer, "Consents", report.Consents)
	writeSection(writer, "Loyalty", report.Loyalty)
	writeSection(writer, "Loyalty members", report.Memberships)
	writeReferralProgram(writer, report.ReferralProgram)
	if len(report.History) > 0 {
		fmt.Fprintln(writer, "History archive (create / unchanged / skip):")
		for _, table := range report.History {
			fmt.Fprintf(writer, "  %-36s %6d %6d %6d\n", table.Table, table.Create, table.Already, table.Skipped)
		}
	}
	if report.Gifts.Unclaimed > 0 {
		fmt.Fprintf(writer, "Unclaimed gifts: %d paid, not received, %s %s — not carried; hand them over or refund them in Bedolaga first\n",
			report.Gifts.Unclaimed, FormatMinor(report.Gifts.SumMinor), report.Currency)
		writeCounts(writer, "by status", report.Gifts.ByStatus)
	}
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

// writeReferralProgram prints the referral terms the run carries, so the
// operator reads what customers will be paid before apply switches it on.
func writeReferralProgram(writer io.Writer, decision *ReferralProgramDecision) {
	if decision == nil {
		return
	}
	terms := decision.Terms
	fmt.Fprintf(writer, "%-14s %s (%s scheme)", "Referrals:", decision.Action, decision.Scheme)
	if len(decision.Codes) > 0 {
		fmt.Fprintf(writer, " — %s", strings.Join(decision.Codes, ", "))
	}
	fmt.Fprintln(writer)
	if decision.Action == ActionSkip && slices.Contains(decision.Codes, CodeReferralProgramPaysNothing) {
		return
	}
	first := "same"
	if terms.FirstPaymentCommissionBPS != nil {
		first = FormatMinor(int64(*terms.FirstPaymentCommissionBPS)) + "%"
	}
	fmt.Fprintf(writer, "  enabled %t; inviter %s + %d days, friend %s + %d days; commission %s%%, first payment %s, %d tiers\n",
		terms.Enabled, FormatMinor(terms.InviterRewardMinor), terms.InviterRewardDays,
		FormatMinor(terms.InviteeRewardMinor), terms.InviteeRewardDays,
		FormatMinor(int64(terms.CommissionBPS)), first, len(terms.CommissionTiers))
	if len(decision.Warnings) > 0 {
		fmt.Fprintf(writer, "  warnings: %s\n", strings.Join(decision.Warnings, ", "))
	}
}
