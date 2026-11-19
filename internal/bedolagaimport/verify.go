package bedolagaimport

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"
)

// VerifiedCustomer is an imported customer as Omniflow holds it now.
type VerifiedCustomer struct {
	CustomerID      string
	Status          string
	TelegramSubject string
}

// VerifiedSubscription is an imported subscription as Omniflow holds it now.
type VerifiedSubscription struct {
	CustomerID        string
	RemnawaveID       int64
	EntitlementStatus string
	EndsAt            time.Time
	HasEntitlement    bool
}

// VerifiedReferral is an imported referral pair as Omniflow holds it now.
type VerifiedReferral struct {
	CustomerID         string
	ReferrerCustomerID string
}

// VerifyInput is an independent read of what the importer produced, taken
// from the Omniflow tables themselves rather than from the item bookkeeping.
type VerifyInput struct {
	Customers     map[int64]VerifiedCustomer
	Subscriptions map[int64]VerifiedSubscription
	Referrals     map[int64]VerifiedReferral
	Wallets       map[int64]WalletMovements
}

// Check is one reconciliation of a source total against the target.
type Check struct {
	Name     string  `json:"name"`
	Expected int64   `json:"expected"`
	Actual   int64   `json:"actual"`
	OK       bool    `json:"ok"`
	Failing  []int64 `json:"failing,omitempty"`
}

// VerifyReport is the result of `verify`.
type VerifyReport struct {
	GeneratedAt time.Time `json:"generatedAt"`
	OK          bool      `json:"ok"`
	Checks      []Check   `json:"checks"`
	// Conflicts are records that were never importable. They do not fail
	// verification: the plan reported them and the operator decided.
	Conflicts int `json:"conflicts"`
}

// Verify reconciles a fresh plan against what Omniflow holds. A plan built
// after a complete apply has nothing left to write; every importable user
// has a customer holding their Telegram ID; every wallet sums to the Bedolaga
// balance; every active subscription has an entitlement ending when Bedolaga
// says it ends, on the same Remnawave user; and every importable referral
// pair points at the right referrer.
func Verify(plan Plan, input VerifyInput, now time.Time) VerifyReport {
	report := VerifyReport{GeneratedAt: now.UTC(), OK: true}
	add := func(check Check) {
		sort.Slice(check.Failing, func(i, j int) bool { return check.Failing[i] < check.Failing[j] })
		if len(check.Failing) > 50 {
			check.Failing = check.Failing[:50]
		}
		check.OK = check.OK && check.Expected == check.Actual
		report.OK = report.OK && check.OK
		report.Checks = append(report.Checks, check)
	}

	pending := Check{Name: "nothing left to apply", Expected: 0, Actual: int64(plan.Pending()), OK: true}
	add(pending)

	customers := map[int64]string{}
	users := Check{Name: "users", OK: true}
	for _, decision := range plan.Users {
		if decision.Action == ActionConflict {
			report.Conflicts++
		}
		if !decision.Importable() {
			continue
		}
		users.Expected++
		held, ok := input.Customers[decision.Source.ID]
		telegram := ""
		if decision.Source.TelegramID != nil {
			telegram = strconv.FormatInt(*decision.Source.TelegramID, 10)
		}
		if !ok || (telegram != "" && held.TelegramSubject != telegram) {
			users.Failing = append(users.Failing, decision.Source.ID)
			continue
		}
		customers[decision.Source.ID] = held.CustomerID
		users.Actual++
	}
	add(users)

	balances := Check{Name: "balance sum (minor units)", OK: true}
	for _, decision := range plan.Balances {
		if decision.Action == ActionConflict {
			report.Conflicts++
			continue
		}
		held := input.Wallets[decision.SourceUserID]
		balances.Expected += decision.SourceMinor
		balances.Actual += held.TotalMinor
		if held.TotalMinor != decision.SourceMinor {
			balances.Failing = append(balances.Failing, decision.SourceUserID)
		}
	}
	balances.OK = len(balances.Failing) == 0
	add(balances)

	subscriptions := Check{Name: "active subscriptions", OK: true}
	for _, decision := range plan.Subscriptions {
		if decision.Action == ActionConflict {
			report.Conflicts++
		}
		if !decision.Active || decision.Action == ActionConflict || decision.Action == ActionSkip {
			continue
		}
		subscriptions.Expected++
		held, ok := input.Subscriptions[decision.Source.ID]
		matches := ok && held.HasEntitlement && held.EndsAt.Equal(decision.Source.EndDate.UTC().Truncate(time.Microsecond)) &&
			decision.Remote != nil && held.RemnawaveID == decision.Remote.ID
		if !matches {
			subscriptions.Failing = append(subscriptions.Failing, decision.Source.ID)
			continue
		}
		subscriptions.Actual++
	}
	add(subscriptions)

	referrals := Check{Name: "referral pairs", OK: true}
	for _, decision := range plan.Referrals {
		if decision.Action == ActionConflict {
			report.Conflicts++
		}
		if decision.Action != ActionCreate && decision.Action != ActionUnchanged {
			continue
		}
		referrals.Expected++
		held, ok := input.Referrals[decision.RefereeID]
		if !ok || held.ReferrerCustomerID == "" || held.ReferrerCustomerID != customers[decision.ReferrerID] {
			referrals.Failing = append(referrals.Failing, decision.RefereeID)
			continue
		}
		referrals.Actual++
	}
	add(referrals)
	return report
}

// WriteText renders the verification for a terminal.
func (report VerifyReport) WriteText(writer io.Writer) {
	fmt.Fprintf(writer, "Bedolaga → Omniflow verification (%s)\n\n", report.GeneratedAt.Format(time.RFC3339))
	for _, check := range report.Checks {
		verdict := "ok"
		if !check.OK {
			verdict = "MISMATCH"
		}
		fmt.Fprintf(writer, "  %-28s expected %-10d actual %-10d %s\n", check.Name, check.Expected, check.Actual, verdict)
		if len(check.Failing) > 0 {
			fmt.Fprintf(writer, "    first Bedolaga IDs: %v\n", check.Failing)
		}
	}
	fmt.Fprintf(writer, "\nRecords never imported because of a conflict: %d (see the plan report)\n", report.Conflicts)
	if report.OK {
		fmt.Fprintln(writer, "Result: OK")
	} else {
		fmt.Fprintln(writer, "Result: MISMATCH")
	}
}
