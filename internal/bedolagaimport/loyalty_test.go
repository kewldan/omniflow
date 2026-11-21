package bedolagaimport

import (
	"testing"
	"time"
)

func int64ptr(value int64) *int64 { return &value }

// Promo groups become a spend programme with a tier per group, the default
// group its bottom rung, and every member keeps their group's tier.
func TestPromoGroupsBecomeALoyaltyProgramme(t *testing.T) {
	source, remote := fixtureSource()
	source.PromoGroups = []PromoGroup{
		{ID: 1, Name: "Любимый пользователь", IsDefault: true},
		{ID: 2, Name: "VIP", ServerPercent: 20, AutoSpentKopeks: int64ptr(300000)},
		{ID: 3, Name: "TOP пользователь", ServerPercent: 10, AutoSpentKopeks: int64ptr(150000)},
		{ID: 4, Name: "Друзья", ServerPercent: 50, TrafficPercent: 30, HasPeriodRules: true},
	}
	at := planNow.Add(-time.Hour)
	source.PromoMembers = []PromoMember{
		{UserID: 1, GroupID: 2, AssignedAt: at, AssignedBy: "admin"},
		{UserID: 2, GroupID: 3, AssignedAt: at, AssignedBy: "auto"},
		{UserID: 2, GroupID: 1, AssignedAt: at},
		{UserID: 6, GroupID: 3, AssignedAt: at},
	}
	plan := BuildPlan(source, remote, fixtureTarget(), fixtureMapping(), planNow)
	if plan.Loyalty == nil || plan.Loyalty.Action != ActionCreate || plan.Loyalty.WindowDays != loyaltyWindowDays {
		t.Fatalf("loyalty %+v", plan.Loyalty)
	}
	tiers := map[string]LoyaltyTier{}
	for _, tier := range plan.Loyalty.Tiers {
		tiers[tier.Code] = tier
	}
	if tiers["group-1"].Threshold != 0 || tiers["group-1"].ManualOnly ||
		tiers["vip"].Threshold != 300000 || tiers["vip"].DiscountBPS != 2000 ||
		tiers["top"].Threshold != 150000 || tiers["top"].DiscountBPS != 1000 ||
		!tiers["group-4"].ManualOnly || tiers["group-4"].TrafficDiscountBPS != 3000 {
		t.Fatalf("tiers %+v", tiers)
	}
	if len(plan.Loyalty.Warnings) != 1 || plan.Loyalty.Warnings[0] != CodePeriodDiscountsDropped {
		t.Fatalf("warnings %v", plan.Loyalty.Warnings)
	}
	actions := map[string]Action{}
	for _, membership := range plan.Memberships {
		actions[membership.TierCode+":"+string(rune('0'+membership.Source.UserID))] = membership.Action
	}
	if actions["vip:1"] != ActionCreate || actions["top:2"] != ActionCreate ||
		actions["group-1:2"] != ActionSkip || actions["top:6"] != ActionSkip {
		t.Fatalf("memberships %v", actions)
	}

	// Once carried, a second plan writes nothing; an operator's own
	// programme is never laid over.
	target := fixtureTarget()
	target.LoyaltyProgramCount = 1
	target.LoyaltyTierCodes = map[string]bool{"group-1": true, "vip": true, "top": true, "group-4": true}
	target.LoyaltyMembers = map[string]bool{"c1:vip": true}
	again := BuildPlan(source, remote, target, fixtureMapping(), planNow)
	if again.Loyalty.Action != ActionUnchanged {
		t.Fatalf("carried programme %+v", again.Loyalty)
	}
	target.LoyaltyTierCodes = map[string]bool{"gold": true}
	own := BuildPlan(source, remote, target, fixtureMapping(), planNow)
	if own.Loyalty.Action != ActionSkip || own.Loyalty.Codes[0] != CodeLoyaltyProgramExists {
		t.Fatalf("operator's programme %+v", own.Loyalty)
	}
	for _, membership := range own.Memberships {
		if membership.Action == ActionCreate {
			t.Fatalf("a membership into a tier the programme lacks: %+v", membership)
		}
	}
}

// Rows of customers who are carried are archived once; rows of customers who
// are not stay behind, and rows of nobody's are kept.
func TestArchiveFollowsTheCustomers(t *testing.T) {
	source, remote := fixtureSource()
	one, six := int64(1), int64(6)
	source.ArchiveRecords = []ArchiveRecord{
		{Table: "transactions", SourceID: "1", UserID: &one, Data: []byte(`{}`)},
		{Table: "transactions", SourceID: "2", UserID: &one, Data: []byte(`{}`)},
		{Table: "transactions", SourceID: "3", UserID: &six, Data: []byte(`{}`)},
		{Table: "monitoring_logs", SourceID: "9", Data: []byte(`{}`)},
	}
	target := fixtureTarget()
	target.ArchivedRecords = map[string]bool{ArchiveKey("transactions", "2"): true}
	plan := BuildPlan(source, remote, target, fixtureMapping(), planNow)
	byTable := map[string]ArchiveDecision{}
	for _, decision := range plan.Archive {
		byTable[decision.Table] = decision
	}
	if len(byTable["transactions"].Create) != 1 || byTable["transactions"].Already != 1 || byTable["transactions"].Skipped != 1 ||
		len(byTable["monitoring_logs"].Create) != 1 {
		t.Fatalf("archive %+v", byTable)
	}
}

func TestSecretColumnsAreRecognised(t *testing.T) {
	for _, column := range []string{"password_hash", "cabinet_password", "auto_login_token", "token", "vless_uuid",
		"subscription_url", "subscription_crypto_link", "ip_address", "email_change_code", "dedup_hash"} {
		if !ArchiveSecretColumns.MatchString(column) {
			t.Errorf("%s is kept", column)
		}
	}
	for _, column := range []string{"referral_code", "country_code", "amount_kopeks", "description", "remnawave_uuid"} {
		if ArchiveSecretColumns.MatchString(column) {
			t.Errorf("%s is dropped", column)
		}
	}
	for _, table := range []string{"system_settings", "web_api_tokens", "users_deactivated_backup_20260913", "users_revived_20260913"} {
		if !ArchiveSkippedTables.MatchString(table) {
			t.Errorf("%s is read", table)
		}
	}
}
