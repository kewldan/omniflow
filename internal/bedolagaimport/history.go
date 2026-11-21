package bedolagaimport

import (
	"sort"
	"time"
)

// Trial claim origins, as stored in trial_claims.origin.
const (
	// TrialOriginTrial is a customer who had a trial in Bedolaga.
	TrialOriginTrial = "bedolaga_trial"
	// TrialOriginHistory is a customer who subscribed or paid in Bedolaga
	// without a trial on record. Bedolaga offered the trial only to somebody
	// with no subscription at all, so they could never have had one there,
	// and a trial here would be the free days Bedolaga refused them.
	TrialOriginHistory = "bedolaga_history"
)

// Codes of the history decisions.
const (
	CodeTrialPlanMissing      = "trial_plan_missing"
	CodePaymentMethodExcluded = "payment_method_excluded"
	CodeNonPositiveAmount     = "non_positive_amount"
)

// LegacyPeriod is a subscription period the importer already carried over.
type LegacyPeriod struct {
	IsTrial  bool
	StartsAt time.Time
	EndsAt   time.Time
}

// TrialDecision is the trial claim one Bedolaga user needs, so a customer who
// already had their free days, or could no longer have had them, does not get
// them again.
type TrialDecision struct {
	SourceUserID int64
	Action       Action
	Codes        []string
	Origin       string
	// ClaimedAt is when the first Bedolaga subscription started, or when the
	// user signed up if Bedolaga kept no subscription for a paying user.
	ClaimedAt time.Time
	PlanID    string
}

// PeriodDecision carries one Bedolaga subscription over as a period of
// history: what the customer had, and when it ended.
type PeriodDecision struct {
	Source   Subscription
	Action   Action
	Codes    []string
	StartsAt time.Time
	EndsAt   time.Time
}

// PaymentDecision carries one completed Bedolaga top-up over as a payment of
// history.
type PaymentDecision struct {
	Source   Payment
	Action   Action
	Codes    []string
	Currency string
}

// decideHistory adds the trial claims, subscription periods, and payments to
// the plan. It runs after the users are decided, and reads only their actions.
func decideHistory(plan *Plan, source Source, target Target, mapping Mapping, userIndex map[int64]int) {
	subscriptions := append([]Subscription(nil), source.Subscriptions...)
	sort.Slice(subscriptions, func(i, j int) bool { return subscriptions[i].ID < subscriptions[j].ID })
	owner := func(userID int64) *UserDecision {
		if index, ok := userIndex[userID]; ok {
			return &plan.Users[index]
		}
		return nil
	}

	type history struct {
		any, trial bool
		first      time.Time
	}
	seen := map[int64]*history{}
	for _, subscription := range subscriptions {
		if subscription.Status == "pending" {
			// An unpaid purchase never served anybody.
			continue
		}
		entry := seen[subscription.UserID]
		if entry == nil {
			entry = &history{}
			seen[subscription.UserID] = entry
		}
		entry.any = true
		entry.trial = entry.trial || subscription.IsTrial || subscription.Status == "trial"
		if start := subscription.StartDate.UTC(); !start.IsZero() && (entry.first.IsZero() || start.Before(entry.first)) {
			entry.first = start
		}
		plan.Periods = append(plan.Periods, decidePeriod(subscription, owner(subscription.UserID), target))
	}

	trialPlan := trialPlanID(mapping, target)
	for _, user := range plan.Users {
		entry := seen[user.Source.ID]
		if (entry == nil || !entry.any) && !user.Source.HasPaid {
			continue
		}
		if !user.Importable() {
			continue
		}
		decision := TrialDecision{SourceUserID: user.Source.ID, Origin: TrialOriginHistory, ClaimedAt: user.Source.CreatedAt.UTC(), PlanID: trialPlan}
		if entry != nil {
			if entry.trial {
				decision.Origin = TrialOriginTrial
			}
			if !entry.first.IsZero() {
				decision.ClaimedAt = entry.first
			}
		}
		switch {
		case user.CustomerID != "" && target.TrialHolders[user.CustomerID]:
			decision.Action = ActionUnchanged
		case trialPlan == "":
			decision.Action, decision.Codes = ActionConflict, []string{CodeTrialPlanMissing}
		default:
			decision.Action = ActionCreate
		}
		plan.Trials = append(plan.Trials, decision)
	}

	payments := append([]Payment(nil), source.Payments...)
	sort.Slice(payments, func(i, j int) bool { return payments[i].ID < payments[j].ID })
	for _, payment := range payments {
		decision := PaymentDecision{Source: payment, Currency: mapping.Currency}
		user := owner(payment.UserID)
		switch {
		case user == nil || !user.Importable():
			decision.Action, decision.Codes = ActionSkip, []string{CodeUserNotImported}
		case payment.AmountKopeks <= 0:
			decision.Action, decision.Codes = ActionSkip, []string{CodeNonPositiveAmount}
		case mapping.PaymentMethodExcluded(payment.Method):
			decision.Action, decision.Codes = ActionSkip, []string{CodePaymentMethodExcluded}
		case target.LegacyPayments[payment.ID]:
			decision.Action = ActionUnchanged
		default:
			decision.Action = ActionCreate
		}
		plan.Payments = append(plan.Payments, decision)
	}
}

func decidePeriod(subscription Subscription, owner *UserDecision, target Target) PeriodDecision {
	decision := PeriodDecision{Source: subscription, StartsAt: subscription.StartDate.UTC(), EndsAt: subscription.EndDate.UTC()}
	if decision.StartsAt.IsZero() || decision.StartsAt.After(decision.EndsAt) {
		decision.StartsAt = decision.EndsAt
	}
	if owner == nil || !owner.Importable() {
		decision.Action, decision.Codes = ActionSkip, []string{CodeUserNotImported}
		return decision
	}
	existing, ok := target.LegacyPeriods[subscription.ID]
	switch {
	case !ok:
		decision.Action = ActionCreate
	case existing.IsTrial != subscription.IsTrial || !existing.StartsAt.Equal(decision.StartsAt) || !existing.EndsAt.Equal(decision.EndsAt):
		decision.Action = ActionRefresh
	default:
		decision.Action = ActionUnchanged
	}
	return decision
}

// trialPlanID is the Omniflow plan a carried-over trial claim names: the plan
// of the mapping's trial rule, or else the catalogue's only trial plan.
func trialPlanID(mapping Mapping, target Target) string {
	for _, rule := range mapping.Plans {
		if !rule.Trial {
			continue
		}
		if version, ok := target.PlanVersions[rule.PlanVersionID]; ok && version.PlanID != "" {
			return version.PlanID
		}
	}
	plans := map[string]bool{}
	for _, version := range target.PlanVersions {
		if version.PlanKind == "trial" && version.PlanID != "" {
			plans[version.PlanID] = true
		}
	}
	if len(plans) != 1 {
		return ""
	}
	for id := range plans {
		return id
	}
	return ""
}
