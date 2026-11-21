package bedolagaimport

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Item kinds, as stored in bedolaga_import_items.
const (
	KindUser         = "user"
	KindSubscription = "subscription"
	KindReferral     = "referral"
)

// Item statuses, as stored in bedolaga_import_items.
const (
	ItemApplied  = "applied"
	ItemConflict = "conflict"
	ItemSkipped  = "skipped"
)

// Action is what a run will do with one record.
type Action string

const (
	// ActionCreate writes the record for the first time.
	ActionCreate Action = "create"
	// ActionRefresh brings an already imported record up to date with the
	// source. Only subscriptions and balances are refreshed.
	ActionRefresh Action = "refresh"
	// ActionUnchanged means the record was imported and nothing moved since.
	ActionUnchanged Action = "unchanged"
	// ActionConflict means the record cannot be imported until somebody acts.
	ActionConflict Action = "conflict"
	// ActionSkip means the record deliberately does not migrate.
	ActionSkip Action = "skip"
)

// Conflict and skip codes. They are stable: the report, the item table, and
// the runbook all name them.
const (
	CodeDeletedInSource          = "deleted_in_source"
	CodeInvalidTelegramID        = "invalid_telegram_id"
	CodeDuplicateTelegramID      = "duplicate_telegram_id"
	CodeTelegramAlreadyLinked    = "telegram_already_linked"
	CodeBlockedPolicyMissing     = "blocked_policy_missing"
	CodeNegativeBalance          = "negative_balance"
	CodeBalanceCurrencyChanged   = "balance_currency_changed"
	CodeUserNotImported          = "user_not_imported"
	CodePendingInSource          = "pending_in_source"
	CodeNoRemnawaveUser          = "subscription_without_remnawave_user"
	CodeRemnawaveUserNotFound    = "remnawave_user_not_found"
	CodeDuplicateRemnawaveUser   = "duplicate_remnawave_user"
	CodeRemnawaveAlreadyLinked   = "remnawave_user_already_linked"
	CodeUnmappedTariff           = "unmapped_tariff"
	CodeUnknownPlanVersion       = "unknown_plan_version"
	CodeExpiredWithoutRemnawave  = "expired_without_remnawave_user"
	CodeSelfReferral             = "self_referral"
	CodeReferrerNotImported      = "referrer_not_imported"
	CodeRefereeNotImported       = "referee_not_imported"
	CodeAlreadyAttributed        = "already_attributed"
	CodeOmniflowOwnsEntitlement  = "entitlement_owned_by_omniflow"
	CodeWriteConflict            = "write_conflict"
	WarnNoSignInIdentity         = "no_sign_in_identity"
	WarnLanguageDefaulted        = "language_defaulted"
	WarnChangedSinceApply        = "changed_since_apply"
	WarnExpiryDiffersFromPanel   = "expiry_differs_from_panel"
	WarnTelegramDiffersFromPanel = "telegram_differs_from_panel"
	WarnPanelStatus              = "panel_status_differs"
	WarnPlanVersionRetired       = "plan_version_retired"
	WarnPlanKindMismatch         = "plan_kind_mismatch"
	WarnBalanceDecreased         = "balance_decreased"
	WarnSeveralSubscriptions     = "several_subscriptions"
	WarnBotBlocked               = "bot_blocked"
)

// Item is one row of bedolaga_import_items as the target holds it.
type Item struct {
	Kind           string
	SourceID       int64
	Status         string
	Codes          []string
	CustomerID     string
	SubscriptionID string
	EntitlementID  string
	Fingerprint    [32]byte
}

// PlanVersion is the part of an Omniflow plan version the decisions read.
type PlanVersion struct {
	ID           string `json:"id"`
	PlanID       string `json:"planId"`
	PlanCode     string `json:"planCode"`
	PlanKind     string `json:"planKind"`
	GraceSeconds int64  `json:"graceSeconds"`
	Retired      bool   `json:"retired"`
}

// WalletMovements is what the importer has already moved into one wallet.
type WalletMovements struct {
	CustomerID string
	Currency   string
	TotalMinor int64
	Count      int
}

// Target is what Omniflow already holds, read at the start of a run.
type Target struct {
	Items map[string]Item
	// TelegramOwners maps a Telegram ID to the customer holding it.
	TelegramOwners map[int64]string
	// RemnawaveOwners maps a Remnawave user ID to the customer owning it.
	RemnawaveOwners map[int64]string
	PlanVersions    map[string]PlanVersion
	// Referrers maps a referred customer to their referrer.
	Referrers map[string]string
	// Wallets maps a Bedolaga user ID to what the importer moved for them.
	Wallets map[int64]WalletMovements
	// TrialHolders is every customer who already holds a trial claim.
	TrialHolders map[string]bool
	// LegacyPeriods maps a Bedolaga subscription ID to the period carried
	// over for it.
	LegacyPeriods map[int64]LegacyPeriod
	// LegacyPayments holds the payments already carried over, by Payment.Key.
	LegacyPayments map[string]bool
	// LegacyDiscounts maps a customer to the carried date of their last
	// Bedolaga discount.
	LegacyDiscounts map[string]time.Time
	// ReferralAliases maps an upper-cased carried referral code to the
	// customer it belongs to.
	ReferralAliases map[string]string
	// AdCampaigns holds the codes of Omniflow's advertising campaigns.
	AdCampaigns map[string]bool
	// CampaignAttributions maps a customer to the code of the campaign they
	// are attributed to.
	CampaignAttributions map[string]string
	// LegacyGoods holds the Bedolaga Stars orders already carried, by ID.
	LegacyGoods map[int64]bool
	// InfoPageLocales holds every "slug:locale" an Omniflow page already has.
	InfoPageLocales map[string]bool
	// LegacyTickets and LegacyConsents hold the Bedolaga tickets and consents
	// already carried, by ID.
	LegacyTickets  map[int64]bool
	LegacyConsents map[int64]bool
}

// ItemKey is the map key of Target.Items.
func ItemKey(kind string, sourceID int64) string {
	return kind + ":" + strconv.FormatInt(sourceID, 10)
}

// UserDecision is what happens to one Bedolaga user.
type UserDecision struct {
	Source   User
	Action   Action
	Codes    []string
	Warnings []string
	// CustomerID is the Omniflow customer an applied user already became.
	CustomerID string
	Locale     string
	Status     string
	// PrimaryRemnawaveID is the Remnawave user the customer-level mapping
	// carries, which the bot reads to tell a provisioned customer apart.
	PrimaryRemnawaveID int64
	// BotBlocked records that the customer blocked the bot, so delivery
	// passes skip their chat until they write to the bot again.
	BotBlocked  bool
	Fingerprint [32]byte
}

// Importable reports whether the user exists, or will, in Omniflow.
func (decision UserDecision) Importable() bool {
	return decision.Action == ActionCreate || decision.Action == ActionUnchanged
}

// BalanceDecision is the wallet movement one user needs.
type BalanceDecision struct {
	SourceUserID  int64
	Action        Action
	Codes         []string
	Warnings      []string
	Currency      string
	SourceMinor   int64
	ImportedMinor int64
	DeltaMinor    int64
	// Sequence numbers the movement so each one has its own reference.
	Sequence int
}

// SubscriptionDecision is what happens to one Bedolaga subscription.
type SubscriptionDecision struct {
	Source   Subscription
	Action   Action
	Codes    []string
	Warnings []string
	Remote   *RemoteUser
	// Active is true when the subscription carries an entitlement: it is
	// active, a trial, or limited in Bedolaga and its end is in the future.
	// An inactive subscription with a live Remnawave user is still linked, so
	// the customer's access link survives a later renewal.
	Active            bool
	PlanVersion       PlanVersion
	Label             string
	StartsAt          time.Time
	EndsAt            time.Time
	EntitlementStatus string
	TrafficBytes      *int64
	DeviceLimit       *int
	SquadIDs          []string
	Existing          *Item
	Fingerprint       [32]byte
}

// ReferralDecision is what happens to one referrer → referee pair.
type ReferralDecision struct {
	RefereeID   int64
	ReferrerID  int64
	Action      Action
	Codes       []string
	Code        string
	Qualified   bool
	CreatedAt   time.Time
	Fingerprint [32]byte
}

// Plan is every decision of one run, and the report summarising them.
type Plan struct {
	Users         []UserDecision
	Balances      []BalanceDecision
	Subscriptions []SubscriptionDecision
	Referrals     []ReferralDecision
	Trials        []TrialDecision
	Periods       []PeriodDecision
	Payments      []PaymentDecision
	Discounts     []DiscountDecision
	Aliases       []AliasDecision
	Campaigns     []CampaignDecision
	Attributions  []AttributionDecision
	Goods         []GoodsDecision
	Pages         []PageDecision
	Tickets       []TicketDecision
	Consents      []ConsentDecision
	Report        Report
}

// BuildPlan decides what to do with every record. It is deterministic for a
// given input, so plan, apply, and verify read the same decisions.
func BuildPlan(source Source, remote map[int64]RemoteUser, target Target, mapping Mapping, now time.Time) Plan {
	now = now.UTC()
	plan := Plan{}
	users := append([]User(nil), source.Users...)
	sort.Slice(users, func(i, j int) bool { return users[i].ID < users[j].ID })
	subscriptions := append([]Subscription(nil), source.Subscriptions...)
	sort.Slice(subscriptions, func(i, j int) bool { return subscriptions[i].ID < subscriptions[j].ID })
	tariffs := map[int64]Tariff{}
	for _, tariff := range source.Tariffs {
		tariffs[tariff.ID] = tariff
	}

	telegramCount := map[int64]int{}
	for _, user := range users {
		if user.Status != SourceStatusDeleted && user.TelegramID != nil {
			telegramCount[*user.TelegramID]++
		}
	}
	userIndex := map[int64]int{}
	for _, user := range users {
		decision := decideUser(user, telegramCount, target, mapping)
		userIndex[user.ID] = len(plan.Users)
		plan.Users = append(plan.Users, decision)
	}

	subscriptionsPerUser := map[int64]int{}
	for _, subscription := range subscriptions {
		subscriptionsPerUser[subscription.UserID]++
	}
	remoteCount := map[int64]int{}
	for _, subscription := range subscriptions {
		if id := remoteIDFor(subscription, users, userIndex, subscriptionsPerUser); id != 0 && subscription.Status != "pending" {
			remoteCount[id]++
		}
	}
	importedSubscriptions := map[int64]int{}
	for _, subscription := range subscriptions {
		var owner *UserDecision
		if index, ok := userIndex[subscription.UserID]; ok {
			owner = &plan.Users[index]
		}
		remoteID := remoteIDFor(subscription, users, userIndex, subscriptionsPerUser)
		decision := decideSubscription(subscription, owner, remoteID, remote, remoteCount, target, mapping, tariffs, now)
		if decision.Action == ActionCreate || decision.Action == ActionRefresh || decision.Action == ActionUnchanged {
			importedSubscriptions[subscription.UserID]++
		}
		plan.Subscriptions = append(plan.Subscriptions, decision)
	}
	assignPrimaryRemnawave(plan.Users, plan.Subscriptions, userIndex)
	for index := range plan.Users {
		if importedSubscriptions[plan.Users[index].Source.ID] > 1 {
			plan.Users[index].Warnings = append(plan.Users[index].Warnings, WarnSeveralSubscriptions)
		}
	}

	for _, decision := range plan.Users {
		if balance, ok := decideBalance(decision, target, mapping); ok {
			plan.Balances = append(plan.Balances, balance)
		}
	}
	for _, user := range users {
		if user.ReferredByID == nil || user.Status == SourceStatusDeleted {
			continue
		}
		plan.Referrals = append(plan.Referrals, decideReferral(user, plan.Users, userIndex, target))
	}
	decideHistory(&plan, source, target, mapping, userIndex)
	decideCampaigns(&plan, source, target, userIndex)
	decideGoods(&plan, source, target, mapping, userIndex)
	decidePages(&plan, source, target)
	decideTickets(&plan, source, target, userIndex)
	decideConsents(&plan, source, target, userIndex)
	plan.Report = buildReport(plan, source, tariffs, mapping, now)
	for _, version := range target.PlanVersions {
		plan.Report.Available = append(plan.Report.Available, version)
	}
	sort.Slice(plan.Report.Available, func(i, j int) bool {
		left, right := plan.Report.Available[i], plan.Report.Available[j]
		if left.PlanCode != right.PlanCode {
			return left.PlanCode < right.PlanCode
		}
		return left.ID < right.ID
	})
	return plan
}

func decideUser(user User, telegramCount map[int64]int, target Target, mapping Mapping) UserDecision {
	decision := UserDecision{Source: user, Locale: localeFor(user.Language), Status: "active"}
	decision.Fingerprint = userFingerprint(user)
	if user.Language != "" && !strings.EqualFold(user.Language, "ru") && !strings.EqualFold(user.Language, "en") {
		decision.Warnings = append(decision.Warnings, WarnLanguageDefaulted)
	}
	if user.TelegramID == nil && !(user.EmailVerified && user.Email != "") {
		decision.Warnings = append(decision.Warnings, WarnNoSignInIdentity)
	}
	if item, ok := target.Items[ItemKey(KindUser, user.ID)]; ok && item.Status == ItemApplied {
		decision.Action = ActionUnchanged
		decision.CustomerID = item.CustomerID
		if item.Fingerprint != decision.Fingerprint {
			decision.Warnings = append(decision.Warnings, WarnChangedSinceApply)
		}
		return decision
	}
	if user.Status == SourceStatusDeleted {
		decision.Action, decision.Codes = ActionSkip, []string{CodeDeletedInSource}
		return decision
	}
	if user.TelegramID != nil {
		switch telegramID := *user.TelegramID; {
		case telegramID <= 0:
			decision.Codes = append(decision.Codes, CodeInvalidTelegramID)
		case telegramCount[telegramID] > 1:
			decision.Codes = append(decision.Codes, CodeDuplicateTelegramID)
		case target.TelegramOwners[telegramID] != "":
			decision.Codes = append(decision.Codes, CodeTelegramAlreadyLinked)
		}
	}
	if user.Status == SourceStatusBlocked {
		switch mapping.BlockedUsers {
		case BlockedSuspend:
			decision.Status = "suspended"
		case BlockedActive:
		case BlockedBotBlocked:
			// Only a Telegram account can have blocked the bot.
			if user.TelegramID != nil {
				decision.BotBlocked = true
				decision.Warnings = append(decision.Warnings, WarnBotBlocked)
			}
		default:
			decision.Codes = append(decision.Codes, CodeBlockedPolicyMissing)
		}
	}
	if len(decision.Codes) > 0 {
		decision.Action = ActionConflict
		return decision
	}
	decision.Action = ActionCreate
	return decision
}

func localeFor(language string) string {
	if strings.EqualFold(strings.TrimSpace(language), "en") {
		return "en"
	}
	return "ru"
}

// remoteIDFor is the Remnawave user a subscription is served by. Before
// multi-tariff Bedolaga kept it on the user only, so a user's single
// subscription falls back to the user-level ID.
func remoteIDFor(subscription Subscription, users []User, userIndex map[int64]int, perUser map[int64]int) int64 {
	if subscription.RemnawaveID != nil && *subscription.RemnawaveID > 0 {
		return *subscription.RemnawaveID
	}
	index, ok := userIndex[subscription.UserID]
	if !ok || perUser[subscription.UserID] != 1 {
		return 0
	}
	if id := users[index].RemnawaveID; id != nil && *id > 0 {
		return *id
	}
	return 0
}

func decideSubscription(
	subscription Subscription, owner *UserDecision, remoteID int64, remote map[int64]RemoteUser,
	remoteCount map[int64]int, target Target, mapping Mapping, tariffs map[int64]Tariff, now time.Time,
) SubscriptionDecision {
	decision := SubscriptionDecision{Source: subscription, Label: "Subscription"}
	if subscription.TariffID != nil {
		if tariff, ok := tariffs[*subscription.TariffID]; ok && strings.TrimSpace(tariff.Name) != "" {
			decision.Label = truncateRunes(strings.TrimSpace(tariff.Name), 40)
		}
	}
	switch subscription.Status {
	case "active", "trial", "limited":
		decision.Active = subscription.EndDate.After(now)
	}
	if user, ok := remote[remoteID]; ok && remoteID != 0 {
		copied := user
		decision.Remote = &copied
	}
	if item, ok := target.Items[ItemKey(KindSubscription, subscription.ID)]; ok && item.Status == ItemApplied {
		copied := item
		decision.Existing = &copied
	}
	conflict := func(code string) SubscriptionDecision {
		decision.Action, decision.Codes = ActionConflict, []string{code}
		return decision
	}
	skip := func(code string) SubscriptionDecision {
		decision.Action, decision.Codes = ActionSkip, []string{code}
		return decision
	}

	if decision.Existing == nil {
		switch {
		case owner == nil || !owner.Importable():
			if owner != nil && owner.Action == ActionSkip {
				return skip(CodeUserNotImported)
			}
			return conflict(CodeUserNotImported)
		case subscription.Status == "pending":
			return skip(CodePendingInSource)
		case remoteID == 0 && decision.Active:
			return conflict(CodeNoRemnawaveUser)
		case remoteID == 0:
			return skip(CodeExpiredWithoutRemnawave)
		case decision.Remote == nil && decision.Active:
			return conflict(CodeRemnawaveUserNotFound)
		case decision.Remote == nil:
			return skip(CodeRemnawaveUserNotFound)
		case remoteCount[remoteID] > 1:
			return conflict(CodeDuplicateRemnawaveUser)
		}
		if holder := target.RemnawaveOwners[remoteID]; holder != "" && holder != owner.CustomerID {
			return conflict(CodeRemnawaveAlreadyLinked)
		}
	} else if decision.Remote == nil {
		// The panel lost the user after it was imported. What exists in
		// Omniflow stays; reconciliation reports the missing remote user.
		decision.Action = ActionUnchanged
		decision.Warnings = append(decision.Warnings, CodeRemnawaveUserNotFound)
		decision.Fingerprint = decision.Existing.Fingerprint
		return decision
	}

	if decision.Active {
		versionID := mapping.Resolve(subscription)
		if versionID == "" {
			if decision.Existing != nil && decision.Existing.EntitlementID != "" {
				decision.Action = ActionUnchanged
				decision.Fingerprint = decision.Existing.Fingerprint
				decision.Warnings = append(decision.Warnings, CodeUnmappedTariff)
				return decision
			}
			return conflict(CodeUnmappedTariff)
		}
		version, ok := target.PlanVersions[versionID]
		if !ok {
			return conflict(CodeUnknownPlanVersion)
		}
		decision.PlanVersion = version
		if version.Retired {
			decision.Warnings = append(decision.Warnings, WarnPlanVersionRetired)
		}
		if subscription.IsTrial != (version.PlanKind == "trial") {
			decision.Warnings = append(decision.Warnings, WarnPlanKindMismatch)
		}
		decision.EndsAt = subscription.EndDate.UTC()
		decision.StartsAt = subscription.StartDate.UTC()
		if decision.StartsAt.IsZero() || !decision.StartsAt.Before(decision.EndsAt) {
			decision.StartsAt = decision.EndsAt.Add(-time.Minute)
		}
		decision.EntitlementStatus = entitlementStatus(decision.Remote.Status)
		if !strings.EqualFold(decision.Remote.Status, "ACTIVE") {
			decision.Warnings = append(decision.Warnings, WarnPanelStatus)
		}
		if decision.Remote.TrafficLimitBytes > 0 {
			traffic := decision.Remote.TrafficLimitBytes
			decision.TrafficBytes = &traffic
		}
		if limit := decision.Remote.DeviceLimit; limit != nil && *limit > 0 {
			devices := *limit
			decision.DeviceLimit = &devices
		}
		decision.SquadIDs = append([]string(nil), decision.Remote.SquadIDs...)
		sort.Strings(decision.SquadIDs)
		pushed := decision.EndsAt.Add(time.Duration(version.GraceSeconds) * time.Second)
		if difference := decision.Remote.ExpireAt.Sub(pushed); difference > time.Minute || difference < -time.Minute {
			decision.Warnings = append(decision.Warnings, WarnExpiryDiffersFromPanel)
		}
	}
	if owner != nil && owner.Source.TelegramID != nil && decision.Remote.TelegramID != nil &&
		*owner.Source.TelegramID != *decision.Remote.TelegramID {
		decision.Warnings = append(decision.Warnings, WarnTelegramDiffersFromPanel)
	}
	decision.Fingerprint = subscriptionFingerprint(decision)
	switch {
	case decision.Existing == nil:
		decision.Action = ActionCreate
	case decision.Existing.Fingerprint == decision.Fingerprint:
		decision.Action = ActionUnchanged
	case !decision.Active:
		// A subscription that has simply run out since it was imported is
		// not news: Omniflow reads the phase from the entitlement's end.
		decision.Action = ActionUnchanged
		decision.Fingerprint = decision.Existing.Fingerprint
	default:
		decision.Action = ActionRefresh
	}
	return decision
}

// entitlementStatus maps the panel's view of a user onto the entitlement that
// mirrors it, the same mapping reconciliation applies.
func entitlementStatus(remoteStatus string) string {
	switch strings.ToUpper(remoteStatus) {
	case "LIMITED":
		return "limited"
	case "DISABLED":
		return "disabled"
	default:
		return "active"
	}
}

func assignPrimaryRemnawave(users []UserDecision, subscriptions []SubscriptionDecision, userIndex map[int64]int) {
	chosen := map[int64]bool{}
	pick := func(onlyActive bool) {
		for _, decision := range subscriptions {
			if decision.Action != ActionCreate || decision.Remote == nil || (onlyActive && !decision.Active) {
				continue
			}
			index, ok := userIndex[decision.Source.UserID]
			if !ok || chosen[decision.Source.UserID] || users[index].Action != ActionCreate {
				continue
			}
			users[index].PrimaryRemnawaveID = decision.Remote.ID
			chosen[decision.Source.UserID] = true
		}
	}
	pick(true)
	pick(false)
}

func decideBalance(user UserDecision, target Target, mapping Mapping) (BalanceDecision, bool) {
	if !user.Importable() {
		return BalanceDecision{}, false
	}
	imported := target.Wallets[user.Source.ID]
	decision := BalanceDecision{
		SourceUserID: user.Source.ID, Currency: mapping.Currency, SourceMinor: user.Source.BalanceKopeks,
		ImportedMinor: imported.TotalMinor, Sequence: imported.Count + 1,
	}
	switch {
	case imported.Count > 0 && imported.Currency != mapping.Currency:
		decision.Action, decision.Codes = ActionConflict, []string{CodeBalanceCurrencyChanged}
	case user.Source.BalanceKopeks < 0:
		decision.Action, decision.Codes = ActionConflict, []string{CodeNegativeBalance}
	default:
		decision.DeltaMinor = user.Source.BalanceKopeks - imported.TotalMinor
		switch {
		case decision.DeltaMinor == 0:
			if imported.Count == 0 {
				return BalanceDecision{}, false
			}
			decision.Action = ActionUnchanged
		case imported.Count == 0:
			decision.Action = ActionCreate
		default:
			decision.Action = ActionRefresh
		}
		if decision.DeltaMinor < 0 {
			decision.Warnings = append(decision.Warnings, WarnBalanceDecreased)
		}
	}
	return decision, true
}

// WalletKind is the ledger transaction type a balance movement is written as:
// the opening balance is a credit, every later difference a correction.
func (decision BalanceDecision) WalletKind() string {
	if decision.Sequence == 1 && decision.DeltaMinor > 0 {
		return "credit"
	}
	return "correction"
}

// Reference is the ledger reference of the movement. It names the Bedolaga
// user and numbers the movement, so the reference is unique per movement and
// the importer can sum what it moved from the ledger alone.
func (decision BalanceDecision) Reference() string {
	return fmt.Sprintf("bedolaga-user:%d:%d", decision.SourceUserID, decision.Sequence)
}

// IdempotencyKey binds the movement to the exact amounts it reconciles, so a
// retried run whose first attempt already committed is refused as a replay
// with the same parameters rather than credited twice.
func (decision BalanceDecision) IdempotencyKey() string {
	return fmt.Sprintf("bedolaga:balance:%d:%d:%d->%d", decision.SourceUserID, decision.Sequence,
		decision.ImportedMinor, decision.SourceMinor)
}

func decideReferral(user User, users []UserDecision, userIndex map[int64]int, target Target) ReferralDecision {
	decision := ReferralDecision{RefereeID: user.ID, ReferrerID: *user.ReferredByID, Qualified: user.HasPaid, CreatedAt: user.CreatedAt}
	decision.Fingerprint = sha256.Sum256([]byte(fmt.Sprintf("referral|%d|%d", decision.RefereeID, decision.ReferrerID)))
	decision.Code = "bedolaga"
	if index, ok := userIndex[decision.ReferrerID]; ok && users[index].Source.ReferralCode != "" {
		decision.Code = truncateRunes(users[index].Source.ReferralCode, 64)
	}
	if item, ok := target.Items[ItemKey(KindReferral, user.ID)]; ok && item.Status == ItemApplied {
		decision.Action = ActionUnchanged
		return decision
	}
	conflict := func(code string) ReferralDecision {
		decision.Action, decision.Codes = ActionConflict, []string{code}
		return decision
	}
	if decision.RefereeID == decision.ReferrerID {
		decision.Action, decision.Codes = ActionSkip, []string{CodeSelfReferral}
		return decision
	}
	referee := users[userIndex[user.ID]]
	if !referee.Importable() {
		return conflict(CodeRefereeNotImported)
	}
	referrerIndex, ok := userIndex[decision.ReferrerID]
	if !ok || !users[referrerIndex].Importable() {
		return conflict(CodeReferrerNotImported)
	}
	if referee.CustomerID != "" {
		if existing, attributed := target.Referrers[referee.CustomerID]; attributed {
			if existing != users[referrerIndex].CustomerID || existing == "" {
				return conflict(CodeAlreadyAttributed)
			}
		}
	}
	decision.Action = ActionCreate
	return decision
}

func userFingerprint(user User) [32]byte {
	telegram := ""
	if user.TelegramID != nil {
		telegram = strconv.FormatInt(*user.TelegramID, 10)
	}
	return sha256.Sum256([]byte(strings.Join([]string{
		"user", telegram, user.Username, user.FirstName, user.LastName, user.Status,
		strings.ToLower(user.Language), strings.ToLower(user.Email), strconv.FormatBool(user.EmailVerified),
	}, "\x1f")))
}

func subscriptionFingerprint(decision SubscriptionDecision) [32]byte {
	parts := []string{"subscription", strconv.FormatBool(decision.Active), decision.PlanVersion.ID}
	if decision.Remote != nil {
		parts = append(parts, strconv.FormatInt(decision.Remote.ID, 10))
	}
	if decision.Active {
		parts = append(parts, decision.EndsAt.UTC().Format(time.RFC3339), decision.EntitlementStatus,
			optionalInt64(decision.TrafficBytes), optionalInt(decision.DeviceLimit), strings.Join(decision.SquadIDs, ","))
	}
	return sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
}

func optionalInt64(value *int64) string {
	if value == nil {
		return "-"
	}
	return strconv.FormatInt(*value, 10)
}

func optionalInt(value *int) string {
	if value == nil {
		return "-"
	}
	return strconv.Itoa(*value)
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
