package bedolagaimport

import (
	"strings"
	"testing"
	"time"
)

func trialDecision(plan Plan, id int64) *TrialDecision {
	for index := range plan.Trials {
		if plan.Trials[index].SourceUserID == id {
			return &plan.Trials[index]
		}
	}
	return nil
}

// Every Bedolaga user who ever had a subscription gets a trial claim, not only
// the ones on a live trial: Bedolaga offered the trial to nobody who had one,
// so an expired trial, a converted trial, and a paid subscription all mean
// the free days are spent.
func TestEveryPastSubscriberHasSpentTheirTrial(t *testing.T) {
	source, remote := fixtureSource()
	longAgo := planNow.AddDate(-1, 0, 0)
	source.Users = append(source.Users,
		User{ID: 20, TelegramID: int64p(1020), Status: "active", CreatedAt: longAgo},
		User{ID: 21, TelegramID: int64p(1021), Status: "active", CreatedAt: longAgo},
		User{ID: 22, TelegramID: int64p(1022), Status: "active", HasPaid: true, CreatedAt: longAgo},
		User{ID: 23, TelegramID: int64p(1023), Status: "active", CreatedAt: longAgo},
		User{ID: 24, TelegramID: int64p(1024), Status: "active", CreatedAt: longAgo},
	)
	trialStart := longAgo.AddDate(0, 0, 3)
	source.Subscriptions = append(source.Subscriptions,
		// An expired trial with no Remnawave user left: skipped as a
		// subscription, still a spent trial.
		Subscription{ID: 40, UserID: 20, Status: "expired", IsTrial: true, StartDate: trialStart, EndDate: trialStart.AddDate(0, 0, 3)},
		// A paid subscription that ran out long ago.
		Subscription{ID: 41, UserID: 21, Status: "expired", TariffID: int64p(1), StartDate: trialStart, EndDate: trialStart.AddDate(0, 1, 0)},
		// A user whose Telegram ID conflicts does not import.
		Subscription{ID: 44, UserID: 6, Status: "expired", IsTrial: true, StartDate: trialStart, EndDate: trialStart.AddDate(0, 0, 3)},
		// An unpaid purchase served nobody.
		Subscription{ID: 43, UserID: 23, Status: "pending", TariffID: int64p(1), StartDate: trialStart, EndDate: trialStart.AddDate(0, 1, 0)},
	)
	plan := BuildPlan(source, remote, fixtureTarget(), fixtureMapping(), planNow)

	if decision := trialDecision(plan, 20); decision == nil || decision.Action != ActionCreate ||
		decision.Origin != TrialOriginTrial || !decision.ClaimedAt.Equal(trialStart) || decision.PlanID != "p-trial" {
		t.Fatalf("an expired trial is a spent trial, dated when it began: %+v", decision)
	}
	if decision := trialDecision(plan, 21); decision == nil || decision.Origin != TrialOriginHistory {
		t.Fatalf("a former subscriber could never have had a trial: %+v", decision)
	}
	if decision := trialDecision(plan, 22); decision == nil || decision.Origin != TrialOriginHistory ||
		!decision.ClaimedAt.Equal(longAgo) {
		t.Fatalf("a payer with no subscription on record still spent it, dated at sign-up: %+v", decision)
	}
	if decision := trialDecision(plan, 23); decision != nil {
		t.Fatalf("an unpaid purchase is no subscription: %+v", decision)
	}
	if decision := trialDecision(plan, 24); decision != nil {
		t.Fatalf("somebody who never subscribed keeps their trial: %+v", decision)
	}
	if decision := trialDecision(plan, 6); decision != nil {
		t.Fatalf("a user who does not import gets no claim: %+v", decision)
	}
	if plan.Report.Trials.ByOrigin[TrialOriginTrial] == 0 || plan.Report.Trials.ByOrigin[TrialOriginHistory] == 0 {
		t.Fatalf("the report counts claims by origin: %+v", plan.Report.Trials)
	}

	// A customer who already holds a claim keeps it.
	target := fixtureTarget()
	target.Items[ItemKey(KindUser, 20)] = Item{Status: ItemApplied, CustomerID: "c20"}
	target.TrialHolders["c20"] = true
	if decision := trialDecision(BuildPlan(source, remote, target, fixtureMapping(), planNow), 20); decision.Action != ActionUnchanged {
		t.Fatalf("an existing claim is left alone: %+v", decision)
	}
}

// With no trial plan to name, the claims cannot be written, and the plan says
// so rather than letting the free days through.
func TestTrialClaimsNeedATrialPlan(t *testing.T) {
	source, remote := fixtureSource()
	mapping := fixtureMapping()
	mapping.Plans = mapping.Plans[:1]
	target := fixtureTarget()
	delete(target.PlanVersions, versionTrial)
	plan := BuildPlan(source, remote, target, mapping, planNow)
	decision := trialDecision(plan, 1)
	if decision == nil || decision.Action != ActionConflict || decision.Codes[0] != CodeTrialPlanMissing {
		t.Fatalf("no trial plan is a conflict: %+v", decision)
	}

	// The catalogue's only trial plan serves when the mapping names none.
	plan = BuildPlan(source, remote, fixtureTarget(), mapping, planNow)
	if decision := trialDecision(plan, 1); decision == nil || decision.PlanID != "p-trial" || decision.Action != ActionCreate {
		t.Fatalf("the only trial plan is used: %+v", decision)
	}
}

// Every subscription becomes a period of history, ended or not, so "expired"
// segments find the customers who left before the migration. A delta run
// moves the dates the previous bot changed.
func TestSubscriptionsBecomePeriods(t *testing.T) {
	source, remote := fixtureSource()
	source.Subscriptions = append(source.Subscriptions, Subscription{ID: 30, UserID: 6, Status: "expired",
		StartDate: planNow.AddDate(0, -2, 0), EndDate: planNow.AddDate(0, -1, 0)})
	plan := BuildPlan(source, remote, fixtureTarget(), fixtureMapping(), planNow)
	byID := map[int64]PeriodDecision{}
	for _, decision := range plan.Periods {
		byID[decision.Source.ID] = decision
	}
	if decision := byID[15]; decision.Action != ActionCreate || decision.EndsAt.After(planNow) {
		t.Fatalf("an expired subscription is carried as an ended period: %+v", decision)
	}
	if decision := byID[30]; decision.Action != ActionSkip || decision.Codes[0] != CodeUserNotImported {
		t.Fatalf("a user who does not import carries no period: %+v", decision)
	}
	if plan.Report.Periods.Ended != 1 {
		t.Fatalf("one ended period: %+v", plan.Report.Periods)
	}

	target := fixtureTarget()
	applyHistory(&target, plan)
	source.Subscriptions[4].EndDate = source.Subscriptions[4].EndDate.Add(48 * time.Hour)
	again := BuildPlan(source, remote, target, fixtureMapping(), planNow)
	for _, decision := range again.Periods {
		want := ActionUnchanged
		switch {
		case decision.Source.ID == 15:
			want = ActionRefresh
		case decision.Source.ID == 30:
			want = ActionSkip
		}
		if decision.Action != want {
			t.Fatalf("period %d: %s, want %s", decision.Source.ID, decision.Action, want)
		}
	}
}

// Completed top-ups become payment history once each. A deposit with no
// method, or an excluded one, is not money a customer paid in.
func TestTopUpsBecomePaymentHistory(t *testing.T) {
	source, remote := fixtureSource()
	paid := planNow.AddDate(0, -2, 0)
	source.Payments = []Payment{
		{ID: 1, UserID: 1, AmountKopeks: 22900, Method: "yookassa", PaidAt: paid},
		{ID: 2, UserID: 1, AmountKopeks: 5000, Method: "Manual", PaidAt: paid},
		{ID: 3, UserID: 2, AmountKopeks: 10000, Method: "", PaidAt: paid},
		{ID: 4, UserID: 6, AmountKopeks: 10000, Method: "cryptobot", PaidAt: paid},
		{ID: 5, UserID: 2, AmountKopeks: 0, Method: "cryptobot", PaidAt: paid},
		{ID: 6, UserID: 2, AmountKopeks: 15000, Method: "telegram_stars", PaidAt: paid},
	}
	plan := BuildPlan(source, remote, fixtureTarget(), fixtureMapping(), planNow)
	want := map[int64]string{
		1: "", 2: CodePaymentMethodExcluded, 3: CodePaymentMethodExcluded,
		4: CodeUserNotImported, 5: CodeNonPositiveAmount, 6: "",
	}
	for _, decision := range plan.Payments {
		code := want[decision.Source.ID]
		switch {
		case code == "" && decision.Action != ActionCreate:
			t.Fatalf("payment %d is carried: %+v", decision.Source.ID, decision)
		case code != "" && (decision.Action != ActionSkip || decision.Codes[0] != code):
			t.Fatalf("payment %d is skipped as %s: %+v", decision.Source.ID, code, decision)
		}
	}
	section := plan.Report.Payments
	if section.CarriedMinor != 37900 || section.Payers != 2 || section.ExcludedByMethod["manual"] != 1 || section.ExcludedByMethod["none"] != 1 {
		t.Fatalf("payment section: %+v", section)
	}
	if plan.Report.Source.Payments != 6 {
		t.Fatalf("source counts every top-up: %+v", plan.Report.Source)
	}

	// An explicit empty list carries every method; the repeat run nothing.
	mapping := fixtureMapping()
	mapping.ExcludedPaymentMethods = []string{}
	plan = BuildPlan(source, remote, fixtureTarget(), mapping, planNow)
	target := fixtureTarget()
	for _, decision := range plan.Payments {
		if decision.Source.ID == 2 && decision.Action != ActionCreate {
			t.Fatalf("with nothing excluded a manual top-up is carried: %+v", decision)
		}
	}
	applyHistory(&target, plan)
	for _, decision := range BuildPlan(source, remote, target, mapping, planNow).Payments {
		if decision.Action == ActionCreate {
			t.Fatalf("a carried payment is not carried twice: %+v", decision)
		}
	}
}

func TestExcludedPaymentMethodsAreValidated(t *testing.T) {
	if _, err := ParseMapping(strings.NewReader("excludedPaymentMethods: [manual, \"\"]\n")); err == nil {
		t.Fatal("an empty method name is refused")
	}
	mapping, err := ParseMapping(strings.NewReader("excludedPaymentMethods: [Manual]\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !mapping.PaymentMethodExcluded("MANUAL") || mapping.PaymentMethodExcluded("balance") {
		t.Fatalf("the list replaces the default and matches case-insensitively: %+v", mapping.ExcludedPaymentMethods)
	}
	defaults, err := ParseMapping(strings.NewReader(""))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !defaults.PaymentMethodExcluded("balance") || defaults.PaymentMethodExcluded("yookassa") {
		t.Fatal("left out, the default list applies")
	}
}

// The last Bedolaga discount is carried once and only ever moves later.
func TestLastDiscountIsCarried(t *testing.T) {
	source, remote := fixtureSource()
	at := planNow.AddDate(0, 0, -20)
	source.Discounts = []Discount{{UserID: 1, At: at}, {UserID: 6, At: at}}
	plan := BuildPlan(source, remote, fixtureTarget(), fixtureMapping(), planNow)
	if len(plan.Discounts) != 2 || plan.Discounts[0].Action != ActionCreate || plan.Discounts[1].Action != ActionSkip {
		t.Fatalf("discounts: %+v", plan.Discounts)
	}
	target := fixtureTarget()
	target.Items[ItemKey(KindUser, 1)] = Item{Status: ItemApplied, CustomerID: customerFor(1)}
	target.LegacyDiscounts[customerFor(1)] = at
	if decision := BuildPlan(source, remote, target, fixtureMapping(), planNow).Discounts[0]; decision.Action != ActionUnchanged {
		t.Fatalf("the same date is unchanged: %+v", decision)
	}
	source.Discounts[0].At = at.AddDate(0, 0, 5)
	if decision := BuildPlan(source, remote, target, fixtureMapping(), planNow).Discounts[0]; decision.Action != ActionRefresh {
		t.Fatalf("a later discount moves the date: %+v", decision)
	}
	target.LegacyDiscounts[customerFor(1)] = at.AddDate(0, 1, 0)
	if decision := BuildPlan(source, remote, target, fixtureMapping(), planNow).Discounts[0]; decision.Action != ActionUnchanged {
		t.Fatalf("an earlier source date never moves it back: %+v", decision)
	}
}

// Gift payments and direct purchases are carried under their own keys, and
// unclaimed gifts are counted and named by ID for the operator.
func TestGiftsAndDirectPurchases(t *testing.T) {
	source, remote := fixtureSource()
	paid := planNow.AddDate(0, -1, 0)
	source.Payments = []Payment{
		{Kind: PaymentTopUp, ID: 7, UserID: 1, AmountKopeks: 10000, Method: "yookassa", PaidAt: paid},
		{Kind: PaymentDirect, ID: 8, UserID: 2, AmountKopeks: 22900, Method: "platega", PaidAt: paid},
		{Kind: PaymentGift, ID: 7, UserID: 1, AmountKopeks: 30000, Method: "yookassa", PaidAt: paid},
	}
	source.Gifts = []Gift{{ID: 7, Status: "pending_activation", AmountKopeks: 30000, PaidAt: paid}, {ID: 9, Status: "paid", AmountKopeks: 1000, PaidAt: paid}}
	plan := BuildPlan(source, remote, fixtureTarget(), fixtureMapping(), planNow)
	for _, decision := range plan.Payments {
		if decision.Action != ActionCreate {
			t.Fatalf("payment %s is carried: %+v", decision.Source.Key(), decision)
		}
	}
	if plan.Payments[0].Source.Key() == plan.Payments[2].Source.Key() {
		t.Fatal("a gift and a transaction with the same row number are different payments")
	}
	payments := plan.Report.Payments
	if payments.ByKind[PaymentGift] != 1 || payments.ByKind[PaymentDirect] != 1 || payments.CarriedMinor != 62900 {
		t.Fatalf("payment section: %+v", payments)
	}
	gifts := plan.Report.Gifts
	if gifts.Unclaimed != 2 || gifts.SumMinor != 31000 || gifts.ByStatus["paid"] != 1 {
		t.Fatalf("gift section: %+v", gifts)
	}
	named := 0
	for _, problem := range plan.Report.Problems {
		if problem.Kind == "gift" && problem.Codes[0] == CodeGiftUnclaimed {
			named++
		}
	}
	if named != 2 {
		t.Fatalf("each unclaimed gift is named by its ID: %+v", plan.Report.Problems)
	}
	if pending := plan.Pending(); pending == 0 {
		t.Fatal("unclaimed gifts are reported, the payments are still to write")
	}
}

// Bedolaga referral codes become aliases, upper-cased; two codes that differ
// only in case, or one somebody else already holds, are not carried.
func TestReferralCodesBecomeAliases(t *testing.T) {
	source, remote := fixtureSource()
	source.Users[0].ReferralCode = "refAbCd1234"
	source.Users[1].ReferralCode = "refabcd1234"
	source.Users[3].ReferralCode = "refQwErTy12"
	source.Users[7].ReferralCode = "ref!bad"
	target := fixtureTarget()
	target.ReferralAliases["REFQWERTY12"] = "someone-else"
	plan := BuildPlan(source, remote, target, fixtureMapping(), planNow)
	byUser := map[int64]AliasDecision{}
	for _, decision := range plan.Aliases {
		byUser[decision.SourceUserID] = decision
	}
	if byUser[1].Action != ActionConflict || byUser[1].Codes[0] != CodeDuplicateReferralCode || byUser[2].Action != ActionConflict {
		t.Fatalf("codes equal but for case are both refused: %+v %+v", byUser[1], byUser[2])
	}
	if byUser[4].Action != ActionConflict || byUser[4].Codes[0] != CodeReferralCodeTaken {
		t.Fatalf("a code another customer holds is refused: %+v", byUser[4])
	}
	if byUser[8].Action != ActionSkip || byUser[8].Codes[0] != CodeInvalidReferralCode {
		t.Fatalf("a code that cannot be stored is skipped: %+v", byUser[8])
	}

	source.Users[1].ReferralCode = "refOther99"
	plan = BuildPlan(source, remote, fixtureTarget(), fixtureMapping(), planNow)
	for _, decision := range plan.Aliases {
		if decision.SourceUserID == 1 && (decision.Action != ActionCreate || decision.Code != "REFABCD1234") {
			t.Fatalf("a unique code is carried upper-cased: %+v", decision)
		}
	}
}
