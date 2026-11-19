package bedolagaimport

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var planNow = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

func fixtureMapping() Mapping {
	return Mapping{Currency: "RUB", BlockedUsers: BlockedSuspend, WalletReason: DefaultWalletReason, Plans: []PlanRule{
		{TariffID: int64p(1), PlanVersionID: versionMonth},
		{Trial: true, PlanVersionID: versionTrial},
	}}
}

func fixtureTarget() Target {
	return Target{
		Items: map[string]Item{}, TelegramOwners: map[int64]string{}, RemnawaveOwners: map[int64]string{},
		Referrers: map[string]string{}, Wallets: map[int64]WalletMovements{},
		PlanVersions: map[string]PlanVersion{
			versionMonth: {ID: versionMonth, PlanID: "p-month", PlanCode: "month", PlanKind: "one_time"},
			versionTrial: {ID: versionTrial, PlanID: "p-trial", PlanCode: "trial", PlanKind: "trial"},
		},
	}
}

func remoteUser(id int64, expire time.Time) RemoteUser {
	devices := 3
	return RemoteUser{ID: id, Username: "user_" + string(rune('a'+id%26)), Status: "ACTIVE", ExpireAt: expire, DeviceLimit: &devices,
		SquadIDs: []string{"9b6f2d1a-1111-4111-8111-000000000001"}}
}

// fixtureSource is a small Bedolaga with one of every interesting case.
func fixtureSource() (Source, map[int64]RemoteUser) {
	end := planNow.AddDate(0, 0, 20)
	start := end.AddDate(0, 0, -30)
	users := []User{
		{ID: 1, TelegramID: int64p(1001), Username: "alice", Status: "active", Language: "ru", BalanceKopeks: 15000, HasPaid: true, CreatedAt: start},
		{ID: 2, TelegramID: int64p(1002), Status: "active", Language: "en", BalanceKopeks: 0, ReferredByID: int64p(1), HasPaid: true, CreatedAt: start},
		{ID: 3, TelegramID: int64p(1003), Status: "active", BalanceKopeks: -500, CreatedAt: start},
		{ID: 4, TelegramID: int64p(1004), Status: "blocked", CreatedAt: start},
		{ID: 5, TelegramID: int64p(1005), Status: "deleted", BalanceKopeks: 900, CreatedAt: start},
		{ID: 6, TelegramID: int64p(2000), Status: "active", CreatedAt: start},
		{ID: 7, TelegramID: int64p(2000), Status: "active", ReferredByID: int64p(1), CreatedAt: start},
		{ID: 8, TelegramID: int64p(1008), Status: "active", Language: "uk", RemnawaveID: int64p(508), CreatedAt: start},
	}
	subscriptions := []Subscription{
		{ID: 11, UserID: 1, Status: "active", TariffID: int64p(1), StartDate: start, EndDate: end, RemnawaveID: int64p(501)},
		{ID: 12, UserID: 2, Status: "trial", IsTrial: true, TariffID: int64p(1), StartDate: start, EndDate: end, RemnawaveID: int64p(502)},
		{ID: 13, UserID: 3, Status: "active", TariffID: int64p(1), StartDate: start, EndDate: end},
		{ID: 14, UserID: 1, Status: "active", TariffID: int64p(9), StartDate: start, EndDate: end, RemnawaveID: int64p(504)},
		{ID: 15, UserID: 2, Status: "expired", TariffID: int64p(1), StartDate: start.AddDate(-1, 0, 0), EndDate: start, RemnawaveID: int64p(505)},
		{ID: 16, UserID: 1, Status: "active", TariffID: int64p(1), StartDate: start, EndDate: end, RemnawaveID: int64p(599)},
		// The pre-multi-tariff layout: only the user knows the panel user.
		{ID: 18, UserID: 8, Status: "active", TariffID: int64p(1), StartDate: start, EndDate: end},
	}
	remote := map[int64]RemoteUser{}
	for _, id := range []int64{501, 502, 504, 505, 508} {
		remote[id] = remoteUser(id, end)
	}
	expired := remote[505]
	expired.Status, expired.ExpireAt = "EXPIRED", start
	remote[505] = expired
	return Source{Users: users, Subscriptions: subscriptions, Tariffs: []Tariff{{ID: 1, Name: "Безлимит"}, {ID: 9, Name: "Old"}}}, remote
}

func userDecision(t *testing.T, plan Plan, id int64) UserDecision {
	t.Helper()
	for _, decision := range plan.Users {
		if decision.Source.ID == id {
			return decision
		}
	}
	t.Fatalf("no decision for user %d", id)
	return UserDecision{}
}

func subscriptionDecision(t *testing.T, plan Plan, id int64) SubscriptionDecision {
	t.Helper()
	for _, decision := range plan.Subscriptions {
		if decision.Source.ID == id {
			return decision
		}
	}
	t.Fatalf("no decision for subscription %d", id)
	return SubscriptionDecision{}
}

func balanceDecision(plan Plan, id int64) (BalanceDecision, bool) {
	for _, decision := range plan.Balances {
		if decision.SourceUserID == id {
			return decision, true
		}
	}
	return BalanceDecision{}, false
}

func TestBuildPlanClassifiesUsers(t *testing.T) {
	source, remote := fixtureSource()
	plan := BuildPlan(source, remote, fixtureTarget(), fixtureMapping(), planNow)

	if decision := userDecision(t, plan, 1); decision.Action != ActionCreate || decision.Locale != "ru" || decision.PrimaryRemnawaveID != 501 {
		t.Fatalf("user 1: %+v", decision)
	}
	if decision := userDecision(t, plan, 2); decision.Locale != "en" || decision.PrimaryRemnawaveID != 502 {
		t.Fatalf("user 2 should keep English and adopt its trial's panel user: %+v", decision)
	}
	if decision := userDecision(t, plan, 4); decision.Action != ActionCreate || decision.Status != "suspended" {
		t.Fatalf("blocked user under the suspend policy: %+v", decision)
	}
	if decision := userDecision(t, plan, 5); decision.Action != ActionSkip || decision.Codes[0] != CodeDeletedInSource {
		t.Fatalf("deleted user must be skipped: %+v", decision)
	}
	for _, id := range []int64{6, 7} {
		if decision := userDecision(t, plan, id); decision.Action != ActionConflict || decision.Codes[0] != CodeDuplicateTelegramID {
			t.Fatalf("user %d shares a Telegram ID and must conflict: %+v", id, decision)
		}
	}
	if decision := userDecision(t, plan, 8); decision.Locale != "ru" || !hasCode(decision.Warnings, WarnLanguageDefaulted) {
		t.Fatalf("an unsupported language falls back to Russian with a warning: %+v", decision)
	}

	noPolicy := fixtureMapping()
	noPolicy.BlockedUsers = ""
	strict := BuildPlan(source, remote, fixtureTarget(), noPolicy, planNow)
	if decision := userDecision(t, strict, 4); decision.Action != ActionConflict || decision.Codes[0] != CodeBlockedPolicyMissing {
		t.Fatalf("a blocked user without a policy must wait for the operator: %+v", decision)
	}

	target := fixtureTarget()
	target.TelegramOwners[1001] = "already-here"
	linked := BuildPlan(source, remote, target, fixtureMapping(), planNow)
	if decision := userDecision(t, linked, 1); decision.Action != ActionConflict || decision.Codes[0] != CodeTelegramAlreadyLinked {
		t.Fatalf("a Telegram ID another customer holds must conflict: %+v", decision)
	}
}

func TestBuildPlanClassifiesSubscriptions(t *testing.T) {
	source, remote := fixtureSource()
	plan := BuildPlan(source, remote, fixtureTarget(), fixtureMapping(), planNow)

	active := subscriptionDecision(t, plan, 11)
	if active.Action != ActionCreate || !active.Active || active.PlanVersion.ID != versionMonth || active.Label != "Безлимит" {
		t.Fatalf("subscription 11: %+v", active)
	}
	if active.DeviceLimit == nil || *active.DeviceLimit != 3 || active.TrafficBytes != nil || active.EntitlementStatus != "active" {
		t.Fatalf("an imported entitlement mirrors the panel's limits: %+v", active)
	}
	if trial := subscriptionDecision(t, plan, 12); trial.PlanVersion.ID != versionTrial || trial.Action != ActionCreate {
		t.Fatalf("subscription 12 is a trial: %+v", trial)
	}
	if decision := subscriptionDecision(t, plan, 13); decision.Action != ActionConflict || decision.Codes[0] != CodeNoRemnawaveUser {
		t.Fatalf("an active subscription without a panel user must conflict: %+v", decision)
	}
	if decision := subscriptionDecision(t, plan, 14); decision.Action != ActionConflict || decision.Codes[0] != CodeUnmappedTariff {
		t.Fatalf("subscription 14 has no mapped tariff: %+v", decision)
	}
	if decision := subscriptionDecision(t, plan, 15); decision.Action != ActionCreate || decision.Active {
		t.Fatalf("an expired subscription with a live panel user is linked without an entitlement: %+v", decision)
	}
	if decision := subscriptionDecision(t, plan, 16); decision.Action != ActionConflict || decision.Codes[0] != CodeRemnawaveUserNotFound {
		t.Fatalf("subscription 16 points at a panel user that does not exist: %+v", decision)
	}
	if decision := subscriptionDecision(t, plan, 18); decision.Action != ActionCreate || decision.Remote == nil || decision.Remote.ID != 508 {
		t.Fatalf("a single subscription falls back to the user's panel ID: %+v", decision)
	}
	if len(plan.Report.UnmappedTariffs) != 1 || *plan.Report.UnmappedTariffs[0].TariffID != 9 {
		t.Fatalf("the report must name the unmapped tariff: %+v", plan.Report.UnmappedTariffs)
	}

	// A subscription of a user who does not import cannot import either.
	source.Users[2].Status = "deleted"
	gone := BuildPlan(source, remote, fixtureTarget(), fixtureMapping(), planNow)
	if decision := subscriptionDecision(t, gone, 13); decision.Action != ActionSkip || decision.Codes[0] != CodeUserNotImported {
		t.Fatalf("a deleted user's subscription is skipped with its user: %+v", decision)
	}

	target := fixtureTarget()
	target.RemnawaveOwners[501] = "someone-else"
	owned := BuildPlan(source, remote, target, fixtureMapping(), planNow)
	if decision := subscriptionDecision(t, owned, 11); decision.Action != ActionConflict || decision.Codes[0] != CodeRemnawaveAlreadyLinked {
		t.Fatalf("a panel user another customer owns must conflict: %+v", decision)
	}
}

func TestNegativeBalanceUserStillImportsWithoutTheBalance(t *testing.T) {
	source, remote := fixtureSource()
	plan := BuildPlan(source, remote, fixtureTarget(), fixtureMapping(), planNow)
	if decision := userDecision(t, plan, 3); decision.Action != ActionCreate {
		t.Fatalf("user 3 itself is importable: %+v", decision)
	}
	balance, ok := balanceDecision(plan, 3)
	if !ok || balance.Action != ActionConflict || balance.Codes[0] != CodeNegativeBalance {
		t.Fatalf("a negative balance must conflict: %+v", balance)
	}
	if decision := subscriptionDecision(t, plan, 13); decision.Codes[0] == CodeUserNotImported {
		t.Fatalf("subscription 13 must be judged on its own once its user imports: %+v", decision)
	}
}

func TestBalanceDecisionsReconcileFromTheLedger(t *testing.T) {
	source, remote := fixtureSource()
	plan := BuildPlan(source, remote, fixtureTarget(), fixtureMapping(), planNow)
	opening, ok := balanceDecision(plan, 1)
	if !ok || opening.Action != ActionCreate || opening.DeltaMinor != 15000 || opening.WalletKind() != "credit" {
		t.Fatalf("opening balance: %+v", opening)
	}
	if opening.Reference() != "bedolaga-user:1:1" || opening.IdempotencyKey() != "bedolaga:balance:1:1:0->15000" {
		t.Fatalf("stable keys: %s %s", opening.Reference(), opening.IdempotencyKey())
	}
	if _, ok := balanceDecision(plan, 2); ok {
		t.Fatal("a zero balance never moves money")
	}

	target := fixtureTarget()
	target.Items[ItemKey(KindUser, 1)] = Item{Kind: KindUser, SourceID: 1, Status: ItemApplied, CustomerID: "c1", Fingerprint: userFingerprint(source.Users[0])}
	target.Wallets[1] = WalletMovements{CustomerID: "c1", Currency: "RUB", TotalMinor: 15000, Count: 1}
	settled := BuildPlan(source, remote, target, fixtureMapping(), planNow)
	if decision, _ := balanceDecision(settled, 1); decision.Action != ActionUnchanged {
		t.Fatalf("a balance already carried over is unchanged: %+v", decision)
	}

	source.Users[0].BalanceKopeks = 9000
	moved := BuildPlan(source, remote, target, fixtureMapping(), planNow)
	decision, _ := balanceDecision(moved, 1)
	if decision.Action != ActionRefresh || decision.DeltaMinor != -6000 || decision.WalletKind() != "correction" || decision.Sequence != 2 {
		t.Fatalf("a balance spent after the first pass is corrected: %+v", decision)
	}
	if !hasCode(decision.Warnings, WarnBalanceDecreased) {
		t.Fatalf("a decrease is called out: %+v", decision)
	}
	if decision.Reference() == opening.Reference() || decision.IdempotencyKey() == opening.IdempotencyKey() {
		t.Fatal("every movement needs its own reference and key")
	}

	target.Wallets[1] = WalletMovements{CustomerID: "c1", Currency: "USD", TotalMinor: 15000, Count: 1}
	if decision, _ := balanceDecision(BuildPlan(source, remote, target, fixtureMapping(), planNow), 1); decision.Action != ActionConflict {
		t.Fatalf("a currency change cannot be reconciled: %+v", decision)
	}
}

func TestReferralDecisions(t *testing.T) {
	source, remote := fixtureSource()
	plan := BuildPlan(source, remote, fixtureTarget(), fixtureMapping(), planNow)
	var fromTwo, fromSeven *ReferralDecision
	for index := range plan.Referrals {
		switch plan.Referrals[index].RefereeID {
		case 2:
			fromTwo = &plan.Referrals[index]
		case 7:
			fromSeven = &plan.Referrals[index]
		}
	}
	if fromTwo == nil || fromTwo.Action != ActionCreate || fromTwo.ReferrerID != 1 || !fromTwo.Qualified {
		t.Fatalf("referee 2 paid in Bedolaga and arrives qualified: %+v", fromTwo)
	}
	if fromSeven == nil || fromSeven.Action != ActionConflict || fromSeven.Codes[0] != CodeRefereeNotImported {
		t.Fatalf("a referee who does not import cannot carry a pair: %+v", fromSeven)
	}
}

func TestReRunIsUnchanged(t *testing.T) {
	source, remote := fixtureSource()
	first := BuildPlan(source, remote, fixtureTarget(), fixtureMapping(), planNow)
	target := fixtureTarget()
	for _, decision := range first.Users {
		if decision.Action == ActionCreate {
			target.Items[ItemKey(KindUser, decision.Source.ID)] = Item{Status: ItemApplied, CustomerID: "c" + string(rune('0'+decision.Source.ID)), Fingerprint: decision.Fingerprint}
		}
	}
	for _, decision := range first.Subscriptions {
		if decision.Action == ActionCreate {
			item := Item{Status: ItemApplied, CustomerID: "c", SubscriptionID: "s", Fingerprint: decision.Fingerprint}
			if decision.Active {
				item.EntitlementID = "e"
			}
			target.Items[ItemKey(KindSubscription, decision.Source.ID)] = item
		}
	}
	for _, decision := range first.Balances {
		if decision.Action == ActionCreate {
			target.Wallets[decision.SourceUserID] = WalletMovements{Currency: "RUB", TotalMinor: decision.SourceMinor, Count: 1}
		}
	}
	for _, decision := range first.Referrals {
		if decision.Action == ActionCreate {
			target.Items[ItemKey(KindReferral, decision.RefereeID)] = Item{Status: ItemApplied}
		}
	}
	second := BuildPlan(source, remote, target, fixtureMapping(), planNow)
	if pending := second.Pending(); pending != 0 {
		t.Fatalf("a re-run after a complete apply must have nothing to write, has %d", pending)
	}

	// The subscription is renewed in Bedolaga before the final delta run.
	renewed := source.Subscriptions[0].EndDate.AddDate(0, 1, 0)
	source.Subscriptions[0].EndDate = renewed
	expired := remote[501]
	expired.ExpireAt = renewed
	remote[501] = expired
	third := BuildPlan(source, remote, target, fixtureMapping(), planNow)
	if decision := subscriptionDecision(t, third, 11); decision.Action != ActionRefresh || !decision.EndsAt.Equal(renewed) {
		t.Fatalf("a renewal after the first pass is refreshed: %+v", decision)
	}

	// Time passes and a subscription simply runs out: nothing to do.
	later := BuildPlan(source, remote, target, fixtureMapping(), planNow.AddDate(0, 3, 0))
	if decision := subscriptionDecision(t, later, 12); decision.Action != ActionUnchanged {
		t.Fatalf("a subscription that ran out after import is unchanged: %+v", decision)
	}
}

func TestReportIsSerialisableAndCarriesNoPersonalData(t *testing.T) {
	source, remote := fixtureSource()
	plan := BuildPlan(source, remote, fixtureTarget(), fixtureMapping(), planNow)
	encoded, err := json.Marshal(plan.Report)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for _, forbidden := range []string{"1001", "alice"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("the report must not carry %q: %s", forbidden, encoded)
		}
	}
	var text bytes.Buffer
	plan.Report.WriteText(&text)
	for _, expected := range []string{"Unmapped tariffs", "tariff 9", "Left in the Bedolaga archive"} {
		if !strings.Contains(text.String(), expected) {
			t.Fatalf("text report lacks %q:\n%s", expected, text.String())
		}
	}
	if strings.Contains(text.String(), "alice") || strings.Contains(text.String(), "1001") {
		t.Fatalf("text report must not carry personal data:\n%s", text.String())
	}
}

func TestVerifyFindsMismatches(t *testing.T) {
	source, remote := fixtureSource()
	source.Users = source.Users[:2]
	source.Subscriptions = source.Subscriptions[:1]
	plan := BuildPlan(source, remote, fixtureTarget(), fixtureMapping(), planNow)
	if report := Verify(plan, VerifyInput{}, planNow); report.OK {
		t.Fatalf("nothing imported cannot verify: %+v", report)
	}
	// Pretend everything was applied, then verify against matching rows.
	target := fixtureTarget()
	for _, decision := range plan.Users {
		target.Items[ItemKey(KindUser, decision.Source.ID)] = Item{Status: ItemApplied, CustomerID: "c" + string(rune('0'+decision.Source.ID)), Fingerprint: decision.Fingerprint}
	}
	for _, decision := range plan.Subscriptions {
		target.Items[ItemKey(KindSubscription, decision.Source.ID)] = Item{Status: ItemApplied, EntitlementID: "e", Fingerprint: decision.Fingerprint}
	}
	target.Items[ItemKey(KindReferral, 2)] = Item{Status: ItemApplied}
	target.Wallets[1] = WalletMovements{Currency: "RUB", TotalMinor: 15000, Count: 1}
	applied := BuildPlan(source, remote, target, fixtureMapping(), planNow)
	input := VerifyInput{
		Customers: map[int64]VerifiedCustomer{1: {CustomerID: "c1", TelegramSubject: "1001"}, 2: {CustomerID: "c2", TelegramSubject: "1002"}},
		Subscriptions: map[int64]VerifiedSubscription{11: {RemnawaveID: 501, HasEntitlement: true,
			EndsAt: source.Subscriptions[0].EndDate}},
		Referrals: map[int64]VerifiedReferral{2: {CustomerID: "c2", ReferrerCustomerID: "c1"}},
		Wallets:   map[int64]WalletMovements{1: {Currency: "RUB", TotalMinor: 15000, Count: 1}},
	}
	if report := Verify(applied, input, planNow); !report.OK {
		t.Fatalf("a matching target must verify: %+v", report)
	}
	input.Wallets[1] = WalletMovements{Currency: "RUB", TotalMinor: 14000, Count: 1}
	report := Verify(applied, input, planNow)
	if report.OK {
		t.Fatal("a balance that does not sum must fail verification")
	}
	var text bytes.Buffer
	report.WriteText(&text)
	if !strings.Contains(text.String(), "MISMATCH") {
		t.Fatalf("text verification must say so:\n%s", text.String())
	}
}

func TestFormatMinor(t *testing.T) {
	for amount, want := range map[int64]string{0: "0.00", 5: "0.05", 15000: "150.00", -12345: "-123.45"} {
		if got := FormatMinor(amount); got != want {
			t.Errorf("FormatMinor(%d) = %q, want %q", amount, got, want)
		}
	}
}
