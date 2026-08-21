package accountcheckout

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// PlanOffer is one purchasable plan as the comparison screen shows it:
// localized wording, an explicit price and currency, the period it covers, and
// what this particular customer may do with it.
//
// Eligibility and the offered operations are computed here rather than in React
// because they are policy, not presentation. A panel that decided for itself
// which buttons to render would be a second implementation of the rules in
// internal/commerce, and the two would eventually disagree in the customer's
// favour or against it — both of which are bugs.
type PlanOffer struct {
	PlanID                string
	PlanVersionID         string
	Code                  string
	Kind                  string
	Name                  string
	Description           string
	SortOrder             int
	BillingPeriod         string
	Duration              time.Duration
	GracePeriod           time.Duration
	TrafficAllowanceBytes *int64
	DeviceLimit           *int32
	Currency              string
	AmountMinor           int64
	RecurringCapable      bool
	// Operations are the lifecycle actions this customer may start against this
	// plan right now: purchase, extension, upgrade, or downgrade.
	Operations []string
	// Eligible reports whether anything at all can be started. IneligibleReason
	// carries the stable machine reason when it cannot, so the panel explains it
	// in the customer's language rather than showing a disabled control with no
	// account of itself.
	Eligible         bool
	IneligibleReason string
	// Held reports that the customer is already entitled to this plan, so the
	// catalogue can mark it as the one they are on rather than presenting every
	// row as an equally unfamiliar choice.
	Held bool
	// ConfigurableSquads tells the panel a selection step comes before payment.
	ConfigurableSquads bool
}

// SelectableSquad is one squad a customer may add to a plan.
type SelectableSquad struct {
	SquadID string
	Label   string
}

// SquadOffer is the plan version's selection policy plus the squads offered.
type SquadOffer struct {
	Selection string
	Minimum   int
	Maximum   *int
	Offered   []SelectableSquad
}

// Configurable reports whether the customer has any choice to make.
func (offer SquadOffer) Configurable() bool {
	return offer.Selection != "" && offer.Selection != "automatic" && len(offer.Offered) > 0
}

// AddonOffer is one add-on that can be bought together with a plan.
type AddonOffer struct {
	AddonID        string
	AddonVersionID string
	Code           string
	Kind           string
	Name           string
	Description    string
	TrafficBytes   *int64
	DeviceSlots    *int32
	SquadCount     int
	MaxQuantity    int
	Proration      commerce.Proration
	Currency       string
	AmountMinor    int64
}

// PromotionOffer is a campaign currently running against a plan.
//
// It names the campaign and what it takes off, and deliberately does not list
// the promo codes that redeem it. A code is a bearer value the operator hands to
// a particular audience; publishing every code on the plan page would hand all
// of them to everybody.
type PromotionOffer struct {
	Code     string
	Kind     string
	Value    int64
	Currency string
	StartsAt time.Time
	EndsAt   time.Time
	// Eligible reports whether this customer satisfies the campaign's audience
	// rules, so a code that could never work for them is not advertised as if it
	// would.
	Eligible bool
}

// PlanDetail is the plan page: the comparison row plus everything the checkout
// will ask about.
type PlanDetail struct {
	PlanOffer
	Squads     SquadOffer
	Addons     []AddonOffer
	Promotions []PromotionOffer
	TermsURL   string
}

// SubscriptionTarget is one subscription a lifecycle flow can act on.
//
// Every renewal, upgrade, and downgrade names its target explicitly once an
// installation allows concurrent subscriptions. Guessing — "the newest", "the
// one they looked at last" — is how a customer ends up extending the wrong one
// and finding out a month later.
type SubscriptionTarget struct {
	ID       string
	Slot     int
	Label    string
	PlanName string
	Status   string
	EndsAt   time.Time
}

const planColumns = `p.id::text, p.code, p.kind, p.sort_order, l.name, l.description,
	v.id::text, v.billing_period, v.duration_seconds, v.traffic_allowance_bytes, v.device_limit,
	v.upgrade_policy, v.downgrade_policy, v.grace_period_seconds, v.trial_eligibility,
	v.recurring_capable, v.squad_selection, pr.currency, pr.amount_minor`

// planRecord is the catalogue row before customer-specific policy is applied.
type planRecord struct {
	offer           PlanOffer
	upgradePolicy   string
	downgradePolicy string
	trialRule       commerce.TrialRule
	squadSelection  string
}

// Plans lists every visible plan priced in the settlement currency, with the
// operations this customer may start against each.
//
// A plan with no price in that currency is omitted rather than shown as
// unavailable, because it cannot be bought here at all and an entry nobody can
// act on is only a question the customer cannot answer.
func (service *Service) Plans(ctx context.Context, customerID, locale string) ([]PlanOffer, error) {
	rows, err := service.store.pool.Query(ctx, `SELECT `+planColumns+`
		FROM plans p
		JOIN plan_localizations l ON l.plan_id = p.id AND l.locale = $1
		JOIN LATERAL (
			SELECT * FROM plan_versions pv
			WHERE pv.plan_id = p.id AND pv.retired_at IS NULL
			ORDER BY pv.version DESC LIMIT 1
		) v ON true
		JOIN plan_prices pr ON pr.plan_version_id = v.id AND pr.currency = $2
		WHERE p.visible AND p.archived_at IS NULL
		ORDER BY p.sort_order, p.code`, normalizeLocale(locale), service.settings.Currency)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]planRecord, 0, 8)
	for rows.Next() {
		record, scanErr := scanPlan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}

	state, err := service.eligibilityContext(ctx, customerID)
	if err != nil {
		return nil, err
	}
	offers := make([]PlanOffer, 0, len(records))
	for _, record := range records {
		offers = append(offers, applyEligibility(record, state))
	}
	return offers, nil
}

// planRecord reads one plan version priced in a named currency.
//
// The currency is a parameter rather than the installation default because an
// open checkout may already have been fixed to whichever currency the chosen
// payment method settles in, and the confirmation screen must show that price
// rather than the one the catalogue was browsed at.
func (store *Store) planRecord(ctx context.Context, planVersionID, locale, currency string) (planRecord, error) {
	record, err := scanPlan(store.pool.QueryRow(ctx, `SELECT `+planColumns+`
		FROM plan_versions v
		JOIN plans p ON p.id = v.plan_id
		JOIN plan_localizations l ON l.plan_id = p.id AND l.locale = $2
		JOIN plan_prices pr ON pr.plan_version_id = v.id AND pr.currency = $3
		WHERE v.id = $1::uuid AND p.archived_at IS NULL`,
		planVersionID, normalizeLocale(locale), currency))
	if errors.Is(err, pgx.ErrNoRows) {
		return planRecord{}, ErrPlanUnavailable
	}
	return record, err
}

// Plan reads one plan version with everything the checkout will ask about.
func (service *Service) Plan(ctx context.Context, customerID, planVersionID, locale string) (PlanDetail, error) {
	record, err := service.store.planRecord(ctx, planVersionID, locale, service.settings.Currency)
	if err != nil {
		return PlanDetail{}, err
	}
	state, err := service.eligibilityContext(ctx, customerID)
	if err != nil {
		return PlanDetail{}, err
	}
	detail := PlanDetail{
		PlanOffer: applyEligibility(record, state),
		TermsURL:  service.settings.TermsURL,
	}
	if detail.Squads, err = service.store.PlanSquads(ctx, record.offer.PlanVersionID, locale); err != nil {
		return PlanDetail{}, err
	}
	detail.ConfigurableSquads = detail.Squads.Configurable()
	if detail.Addons, err = service.store.PlanAddons(
		ctx, record.offer.PlanVersionID, locale, service.settings.Currency,
	); err != nil {
		return PlanDetail{}, err
	}
	if detail.Promotions, err = service.store.Promotions(ctx, customerID, record.offer.PlanID); err != nil {
		return PlanDetail{}, err
	}
	return detail, nil
}

func scanPlan(row pgx.Row) (planRecord, error) {
	var (
		record          planRecord
		sortOrder       int32
		durationSeconds int64
		graceSeconds    int64
		traffic         pgtype.Int8
		deviceLimit     pgtype.Int4
		trialRule       string
	)
	err := row.Scan(&record.offer.PlanID, &record.offer.Code, &record.offer.Kind, &sortOrder,
		&record.offer.Name, &record.offer.Description, &record.offer.PlanVersionID,
		&record.offer.BillingPeriod, &durationSeconds, &traffic, &deviceLimit,
		&record.upgradePolicy, &record.downgradePolicy, &graceSeconds, &trialRule,
		&record.offer.RecurringCapable, &record.squadSelection,
		&record.offer.Currency, &record.offer.AmountMinor)
	if err != nil {
		return planRecord{}, err
	}
	record.offer.SortOrder = int(sortOrder)
	record.offer.Duration = time.Duration(durationSeconds) * time.Second
	record.offer.GracePeriod = time.Duration(graceSeconds) * time.Second
	record.trialRule = commerce.TrialRule(trialRule)
	record.offer.ConfigurableSquads = record.squadSelection != "" && record.squadSelection != "automatic"
	if traffic.Valid {
		value := traffic.Int64
		record.offer.TrafficAllowanceBytes = &value
	}
	if deviceLimit.Valid {
		value := deviceLimit.Int32
		record.offer.DeviceLimit = &value
	}
	return record, nil
}

// heldPlan is one plan the customer is currently entitled to, with the price it
// is carried at, which is what makes "higher" and "lower" mean anything. The
// type itself lives in operations.go beside the rule that compares it.

// eligibility is what the catalogue needs to know about one customer, gathered
// once for the whole page rather than per row.
type eligibility struct {
	customerID string
	// operations is the state the lifecycle rule judges every row against:
	// how many subscriptions exist, which plans are live behind them, and
	// whether the policy has room for one more.
	operations OperationContext
	trial      commerce.TrialRequest
	policy     commerce.SubscriptionPolicy
}

func (service *Service) eligibilityContext(ctx context.Context, customerID string) (eligibility, error) {
	result := eligibility{customerID: customerID, policy: service.orders.SubscriptionPolicy()}
	targets, err := service.store.SubscriptionTargets(ctx, customerID, "en")
	if err != nil {
		return eligibility{}, err
	}
	result.operations.Subscriptions = len(targets)
	if result.operations.Held, err = service.store.HeldPlans(ctx, customerID, service.settings.Currency); err != nil {
		return eligibility{}, err
	}
	result.operations.AdditionalAllowed = len(targets) > 0 &&
		result.policy.AllowAdditional(len(targets), 0, nil) == nil
	if result.trial, err = service.store.TrialContext(ctx, customerID); err != nil {
		return eligibility{}, err
	}
	result.trial.MinimumAccountAge = service.settings.MinimumTrialAccountAge
	return result, nil
}

// HeldPlans reads the plans behind the customer's live entitlements, priced in
// the settlement currency.
//
// Live means the current entitlement of a subscription that has neither been
// superseded nor run out. An expired entitlement is deliberately not held:
// the customer has nothing to move up or down from, only something to buy
// again, and counting it made the catalogue offer an "upgrade" away from a
// plan they no longer paid for while refusing to sell them that plan itself.
//
// A plan whose current version has no price in that currency contributes no
// amount: it is still held, but it cannot be compared, so it only ever produces
// "you already have this" and never an upgrade or downgrade claim.
func (store *Store) HeldPlans(ctx context.Context, customerID, currency string) ([]HeldPlan, error) {
	rows, err := store.pool.Query(ctx, `SELECT DISTINCT v.plan_id::text, COALESCE(pr.amount_minor, 0)
		FROM subscriptions s
		JOIN LATERAL (
			SELECT * FROM entitlements ent
			WHERE ent.subscription_id = s.id AND ent.status <> 'superseded'
			ORDER BY ent.ends_at DESC LIMIT 1
		) e ON true
		JOIN plan_versions v ON v.id = e.plan_version_id
		LEFT JOIN plan_prices pr ON pr.plan_version_id = v.id AND pr.currency = $2
		WHERE s.user_id = $1::uuid AND s.status = 'active'
		  AND e.status NOT IN ('expired', 'failed')
		  AND (e.ends_at IS NULL OR e.ends_at > now())`, customerID, currency)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	held := make([]HeldPlan, 0, 4)
	for rows.Next() {
		var plan HeldPlan
		if err = rows.Scan(&plan.PlanID, &plan.AmountMinor); err != nil {
			return nil, err
		}
		held = append(held, plan)
	}
	return held, rows.Err()
}

// applyEligibility decides what this customer may do with one catalogue row.
//
// It is a pure function of the row and the customer's already-gathered state, so
// the answer the panel renders can be tested without a database and cannot
// depend on the order the catalogue happened to be read in.
//
// It is advisory, not authoritative: the plan's own concurrency cap and the
// serialized subscription count are re-checked inside CreateOrder, under a lock
// this read deliberately does not take. Hiding an impossible action here is a
// courtesy; refusing it there is the guarantee.
func applyEligibility(record planRecord, state eligibility) PlanOffer {
	offer := record.offer
	if record.offer.Kind == "trial" {
		// A trial is the one plan whose availability is a rule rather than a
		// policy, and the rule lives in internal/commerce. The panel is told the
		// reason so it can explain the refusal instead of hiding the plan, which
		// would leave a customer wondering where the free month went.
		request := state.trial
		request.PlanKind, request.Rule = record.offer.Kind, record.trialRule
		reason, err := commerce.EvaluateTrial(request)
		if err != nil {
			offer.Eligible, offer.IneligibleReason = false, reason
			return offer
		}
		offer.Eligible, offer.Operations = true, []string{"purchase"}
		return offer
	}

	// The one lifecycle rule, shared with the bot: see OfferedOperations.
	offer.Operations = OfferedOperations(PlanPricing{
		PlanID: record.offer.PlanID, AmountMinor: record.offer.AmountMinor,
		UpgradePolicy: record.upgradePolicy, DowngradePolicy: record.downgradePolicy,
	}, state.operations)
	offer.Held = state.operations.Holds(record.offer.PlanID)
	offer.Eligible = len(offer.Operations) > 0
	if !offer.Eligible {
		// Nothing applies only when the customer holds something and this plan
		// refuses to be moved to from it; "limit reached" is the closest of the
		// reasons both panels explain, and it says the catalogue, not the
		// customer, is what stands in the way.
		offer.IneligibleReason = commerce.SubscriptionLimitReached
	}
	return offer
}

// PlanSquads reads the squad configurator for one plan version.
func (store *Store) PlanSquads(ctx context.Context, planVersionID, locale string) (SquadOffer, error) {
	var (
		offer   SquadOffer
		minimum int32
		maximum pgtype.Int4
	)
	err := store.pool.QueryRow(ctx, `SELECT squad_selection, min_selectable_squads, max_selectable_squads
		FROM plan_versions WHERE id = $1::uuid`, planVersionID).Scan(&offer.Selection, &minimum, &maximum)
	if errors.Is(err, pgx.ErrNoRows) {
		return SquadOffer{}, ErrPlanUnavailable
	}
	if err != nil {
		return SquadOffer{}, err
	}
	offer.Minimum = int(minimum)
	if maximum.Valid {
		value := int(maximum.Int32)
		offer.Maximum = &value
	}
	// The label column is chosen rather than parameterized because it is a
	// column name, and the value is one of two constants decided here.
	labelColumn := "label_en"
	if normalizeLocale(locale) == "ru" {
		labelColumn = "label_ru"
	}
	rows, err := store.pool.Query(ctx, `SELECT squad_id::text, `+labelColumn+`
		FROM plan_version_squads WHERE plan_version_id = $1::uuid
		ORDER BY sort_order, squad_id`, planVersionID)
	if err != nil {
		return SquadOffer{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var squad SelectableSquad
		if err = rows.Scan(&squad.SquadID, &squad.Label); err != nil {
			return SquadOffer{}, err
		}
		offer.Offered = append(offer.Offered, squad)
	}
	return offer, rows.Err()
}

// PlanAddons lists the add-ons offered for a plan version in one currency.
func (store *Store) PlanAddons(ctx context.Context, planVersionID, locale, currency string) ([]AddonOffer, error) {
	rows, err := store.pool.Query(ctx, `SELECT a.id::text, v.id::text, a.code, a.kind, l.name, l.description,
		v.traffic_bytes, v.device_slots, cardinality(v.remnawave_squad_ids), v.max_quantity, v.proration,
		pr.currency, pr.amount_minor
		FROM plan_version_addons pa
		JOIN addons a ON a.id = pa.addon_id
		JOIN addon_localizations l ON l.addon_id = a.id AND l.locale = $2
		JOIN LATERAL (
			SELECT * FROM addon_versions av
			WHERE av.addon_id = a.id AND av.retired_at IS NULL
			ORDER BY av.version DESC LIMIT 1
		) v ON true
		JOIN addon_prices pr ON pr.addon_version_id = v.id AND pr.currency = $3
		WHERE pa.plan_version_id = $1::uuid AND a.visible AND a.archived_at IS NULL
		ORDER BY a.sort_order, a.code`, planVersionID, normalizeLocale(locale), currency)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	offers := make([]AddonOffer, 0, 6)
	for rows.Next() {
		var (
			offer       AddonOffer
			traffic     pgtype.Int8
			slots       pgtype.Int4
			squadCount  int32
			maxQuantity int32
			proration   string
		)
		if err = rows.Scan(&offer.AddonID, &offer.AddonVersionID, &offer.Code, &offer.Kind, &offer.Name,
			&offer.Description, &traffic, &slots, &squadCount, &maxQuantity, &proration,
			&offer.Currency, &offer.AmountMinor); err != nil {
			return nil, err
		}
		if traffic.Valid {
			value := traffic.Int64
			offer.TrafficBytes = &value
		}
		if slots.Valid {
			value := slots.Int32
			offer.DeviceSlots = &value
		}
		offer.SquadCount, offer.MaxQuantity = int(squadCount), int(maxQuantity)
		offer.Proration = commerce.Proration(proration)
		offers = append(offers, offer)
	}
	return offers, rows.Err()
}

// Promotions lists the campaigns currently running against a plan, marking each
// with whether this customer's own history satisfies its audience rules.
func (store *Store) Promotions(ctx context.Context, customerID, planID string) ([]PromotionOffer, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return nil, err
	}
	// The scoping condition mirrors IsPromotionPlanEligible exactly: a campaign
	// with no plan links applies to every plan, and one with links applies only
	// to those. Listing only the linked ones would hide the installation-wide
	// campaigns, which are the ones most likely to be running.
	rows, err := store.pool.Query(ctx, `SELECT pm.code, pm.kind, pm.value, pm.currency,
		pm.starts_at, pm.ends_at, pm.eligibility
		FROM promotions pm
		WHERE pm.active AND pm.applies_to = 'plans'
		  AND (pm.starts_at IS NULL OR pm.starts_at <= now())
		  AND (pm.ends_at IS NULL OR pm.ends_at > now())
		  AND (NOT EXISTS (SELECT 1 FROM promotion_plans pp WHERE pp.promotion_id = pm.id)
		       OR EXISTS (SELECT 1 FROM promotion_plans pp
		                  WHERE pp.promotion_id = pm.id AND pp.plan_id = $1::uuid))
		ORDER BY pm.code`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type candidate struct {
		offer       PromotionOffer
		eligibility []byte
	}
	candidates := make([]candidate, 0, 4)
	for rows.Next() {
		var (
			entry    candidate
			currency pgtype.Text
			startsAt pgtype.Timestamptz
			endsAt   pgtype.Timestamptz
		)
		if err = rows.Scan(&entry.offer.Code, &entry.offer.Kind, &entry.offer.Value, &currency,
			&startsAt, &endsAt, &entry.eligibility); err != nil {
			return nil, err
		}
		entry.offer.Currency = currency.String
		entry.offer.StartsAt, entry.offer.EndsAt = startsAt.Time.UTC(), endsAt.Time.UTC()
		candidates = append(candidates, entry)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	// Audience eligibility is evaluated by the same query the redemption path
	// uses, so a campaign advertised as available is one the order would accept.
	queries := dbgen.New(store.pool)
	offers := make([]PromotionOffer, 0, len(candidates))
	for _, entry := range candidates {
		eligible, checkErr := queries.CheckPromotionCustomerEligibility(
			ctx, dbgen.CheckPromotionCustomerEligibilityParams{Eligibility: entry.eligibility, UserID: userID},
		)
		if checkErr != nil {
			return nil, checkErr
		}
		entry.offer.Eligible = eligible.Valid && eligible.Bool
		offers = append(offers, entry.offer)
	}
	return offers, nil
}

// SubscriptionTargets lists the subscriptions a lifecycle flow may act on.
func (store *Store) SubscriptionTargets(ctx context.Context, customerID, locale string) ([]SubscriptionTarget, error) {
	rows, err := store.pool.Query(ctx, `SELECT s.id::text, s.slot, s.label,
		COALESCE(l.name, p.code, ''), COALESCE(e.status, ''), e.ends_at
		FROM subscriptions s
		LEFT JOIN LATERAL (
			SELECT * FROM entitlements ent
			WHERE ent.subscription_id = s.id AND ent.status <> 'superseded'
			ORDER BY ent.ends_at DESC LIMIT 1
		) e ON true
		LEFT JOIN plan_versions v ON v.id = e.plan_version_id
		LEFT JOIN plans p ON p.id = v.plan_id
		LEFT JOIN plan_localizations l ON l.plan_id = p.id AND l.locale = $2
		WHERE s.user_id = $1::uuid AND s.status = 'active'
		ORDER BY s.slot`, customerID, normalizeLocale(locale))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := make([]SubscriptionTarget, 0, 4)
	for rows.Next() {
		var (
			target SubscriptionTarget
			slot   int32
			endsAt pgtype.Timestamptz
		)
		if err = rows.Scan(&target.ID, &slot, &target.Label, &target.PlanName, &target.Status, &endsAt); err != nil {
			return nil, err
		}
		target.Slot, target.EndsAt = int(slot), endsAt.Time.UTC()
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

// TrialContext gathers the abuse-control inputs for a trial activation. The
// decision itself belongs to commerce.EvaluateTrial; this only reads the facts.
func (store *Store) TrialContext(ctx context.Context, customerID string) (commerce.TrialRequest, error) {
	var (
		request            commerce.TrialRequest
		createdAt          time.Time
		settledOrders      int64
		activeEntitlements int64
	)
	err := store.pool.QueryRow(ctx, `SELECT u.created_at,
		EXISTS (SELECT 1 FROM trial_claims c JOIN orders co ON co.id = c.order_id
			WHERE c.user_id = u.id AND co.state NOT IN ('cancelled', 'expired')),
		(SELECT count(*) FROM orders o WHERE o.user_id = u.id
			AND o.state IN ('paid','fulfilled','partially_refunded','refunded')),
		(SELECT count(*) FROM entitlements e WHERE e.user_id = u.id
			AND e.status IN ('pending','active','limited') AND e.ends_at > now()),
		EXISTS (
			SELECT 1 FROM identities mine
			JOIN identities other ON other.provider = mine.provider
				AND other.provider_subject = mine.provider_subject AND other.user_id <> mine.user_id
			JOIN trial_claims claimed ON claimed.user_id = other.user_id
			JOIN orders claimed_order ON claimed_order.id = claimed.order_id
				AND claimed_order.state NOT IN ('cancelled', 'expired')
			WHERE mine.user_id = u.id AND mine.status = 'active'
		)
		FROM users u WHERE u.id = $1::uuid`, customerID).
		Scan(&createdAt, &request.AlreadyClaimed, &settledOrders, &activeEntitlements,
			&request.SharedIdentitySignal)
	if err != nil {
		return commerce.TrialRequest{}, err
	}
	request.AccountAge = time.Since(createdAt)
	request.CompletedOrders = int(settledOrders)
	request.ActiveEntitlement = activeEntitlements > 0
	return request, nil
}

// PlanPrices lists every currency a plan version is priced in. Payment-method
// selection needs it because the chosen adapter fixes the order currency.
func (store *Store) PlanPrices(ctx context.Context, planVersionID string) (map[string]int64, error) {
	rows, err := store.pool.Query(ctx, `SELECT currency, amount_minor FROM plan_prices
		WHERE plan_version_id = $1::uuid ORDER BY currency`, planVersionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	prices := make(map[string]int64, 4)
	for rows.Next() {
		var (
			currency string
			amount   int64
		)
		if err = rows.Scan(&currency, &amount); err != nil {
			return nil, err
		}
		prices[currency] = amount
	}
	return prices, rows.Err()
}

// planKind reads the two catalogue facts a confirmation needs that the checkout
// session does not carry: whether the plan is a trial, and under which rule.
func (store *Store) planKind(ctx context.Context, planVersionID string) (string, commerce.TrialRule, error) {
	var kind, rule string
	err := store.pool.QueryRow(ctx, `SELECT p.kind, v.trial_eligibility
		FROM plan_versions v JOIN plans p ON p.id = v.plan_id
		WHERE v.id = $1::uuid`, planVersionID).Scan(&kind, &rule)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrPlanUnavailable
	}
	return kind, commerce.TrialRule(rule), err
}

func parseUUID(value string) (pgtype.UUID, error) {
	var parsed pgtype.UUID
	if err := parsed.Scan(value); err != nil || !parsed.Valid {
		return pgtype.UUID{}, invalidInput("that identifier is not valid")
	}
	return parsed, nil
}
