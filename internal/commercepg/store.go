package commercepg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/commerce"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

type EnqueueFulfillment func(context.Context, pgx.Tx, string) error

// Options are the operator-configured policies the store enforces. They are
// supplied by each process from configuration, so the API, the bot, and the
// worker all apply the same limits.
type Options struct {
	Subscriptions commerce.SubscriptionPolicy
	TopUp         commerce.TopUpLimits
	Logger        *slog.Logger
}

type Store struct {
	pool    *pgxpool.Pool
	enqueue EnqueueFulfillment
	clock   func() time.Time
	options Options
}

func New(pool *pgxpool.Pool, enqueue EnqueueFulfillment, options ...Options) *Store {
	store := &Store{pool: pool, enqueue: enqueue, clock: time.Now}
	if len(options) > 0 {
		store.options = options[0]
	}
	if store.options.Logger == nil {
		store.options.Logger = slog.Default()
	}
	return store
}

// SubscriptionPolicy reports the concurrency policy this store enforces.
func (store *Store) SubscriptionPolicy() commerce.SubscriptionPolicy {
	return store.options.Subscriptions
}

// TopUpLimits reports the wallet top-up policy this store enforces.
func (store *Store) TopUpLimits() commerce.TopUpLimits { return store.options.TopUp }

func (store *Store) RunMaintenance(ctx context.Context, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		store.expirePendingOrders(ctx)
		store.expireWalletCredits(ctx)
		// A saved cart is charged as soon as any wallet credit covers it, not
		// only when a top-up settles, so a referral reward or an operator
		// correction releases it too.
		store.SweepCarts(ctx, logger)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// expirePendingOrders closes orders whose payment window elapsed and removes
// the subscriptions those orders had opened and nothing else ever used.
func (store *Store) expirePendingOrders(ctx context.Context) {
	expired, err := dbgen.New(store.pool).ExpirePendingOrders(ctx)
	if err != nil {
		return
	}
	customers := make(map[pgtype.UUID]struct{}, len(expired))
	for _, order := range expired {
		if order.SubscriptionID.Valid {
			customers[order.UserID] = struct{}{}
		}
	}
	for userID := range customers {
		if _, err := store.ReleaseGhostSubscriptions(ctx, uuidString(userID)); err != nil {
			store.options.Logger.Warn("ghost subscription cleanup after expiry failed", "error", err)
		}
	}
}

// CancelOrder closes an order that has not been paid, records the mutation
// under the caller's idempotency key, and removes any subscription the order
// had opened that nothing else uses. A repeated call with the same key is a
// no-op. It is the cancel path for an operator; a customer surface with its
// own cancel statement calls ReleaseGhostSubscriptions after it commits.
func (store *Store) CancelOrder(ctx context.Context, orderID, idempotencyKey, reason string) (dbgen.Order, error) {
	id, err := parseUUID(orderID)
	if err != nil {
		return dbgen.Order{}, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return dbgen.Order{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	order, err := queries.CancelOrder(ctx, dbgen.CancelOrderParams{OrderID: id, IdempotencyKey: idempotencyKey, Reason: optionalText(reason)})
	if err != nil {
		return dbgen.Order{}, err
	}
	if order.SubscriptionID.Valid {
		if _, err = store.releaseGhostSubscriptions(ctx, queries, order.UserID); err != nil {
			return dbgen.Order{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return dbgen.Order{}, err
	}
	return order, nil
}

// ReleaseGhostSubscriptions removes a customer's subscriptions that exist only
// because an order once opened them and that order then closed unpaid. It is
// safe to call after any cancellation or expiry, and a customer with nothing
// to release is a no-op. The number of rows removed is returned.
func (store *Store) ReleaseGhostSubscriptions(ctx context.Context, customerID string) (int, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return 0, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	released, err := store.releaseGhostSubscriptions(ctx, dbgen.New(tx), userID)
	if err != nil {
		return 0, err
	}
	return released, tx.Commit(ctx)
}

// releaseGhostSubscriptions is the transactional core. The customer's
// subscriptions are locked the same way a purchase locks them, so a checkout
// resolving its target and this cleanup cannot interleave.
func (store *Store) releaseGhostSubscriptions(ctx context.Context, queries *dbgen.Queries, userID pgtype.UUID) (int, error) {
	if err := queries.LockCustomerSubscriptions(ctx, uuidString(userID)); err != nil {
		return 0, err
	}
	removed, err := queries.DeleteGhostSubscriptions(ctx, userID)
	if err != nil {
		return 0, err
	}
	return len(removed), nil
}

func (store *Store) expireWalletCredits(ctx context.Context) {
	entries, err := dbgen.New(store.pool).ListExpiredWalletCredits(ctx, 500)
	if err != nil {
		return
	}
	for _, entry := range entries {
		tx, beginErr := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
		if beginErr != nil {
			return
		}
		queries := dbgen.New(tx)
		key := "expiration:" + uuidString(entry.ID)
		if _, existingErr := queries.GetLedgerTransactionByIdempotency(ctx, key); existingErr == nil {
			_ = tx.Commit(ctx)
			continue
		} else if !errors.Is(existingErr, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			continue
		}
		transaction, createErr := queries.CreateLedgerTransaction(ctx, dbgen.CreateLedgerTransactionParams{Type: "expiration", ReferenceType: "ledger_entry", ReferenceID: uuidString(entry.ID), IdempotencyKey: key})
		if createErr == nil && entry.AmountToExpire > 0 {
			_, createErr = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "customer_wallet", UserID: entry.UserID, Currency: entry.Currency, AmountMinor: -entry.AmountToExpire})
		}
		if createErr == nil && entry.AmountToExpire > 0 {
			_, createErr = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "platform_clearing", Currency: entry.Currency, AmountMinor: entry.AmountToExpire})
		}
		if createErr != nil {
			_ = tx.Rollback(ctx)
			continue
		}
		_ = tx.Commit(ctx)
	}
}

type CreateOrderInput struct {
	CustomerID     string
	PlanVersionID  string
	Currency       string
	Operation      string
	PromoCode      string
	IdempotencyKey string
	// ExpiresAt is when the payment window closes. Zero lets the store choose
	// from Provider.
	ExpiresAt time.Time
	// Provider is the payment provider the customer already chose, when the
	// surface knows it at order time. It only sets the payment window: a
	// manual transfer is given days rather than an hour. A surface that picks
	// the provider later leaves it empty, and the window is extended when the
	// intent is created.
	Provider string
	// SkipWallet keeps the customer's wallet balance out of this order. The
	// zero value applies the wallet first, which is the documented default.
	SkipWallet bool
	// SubscriptionID names the subscription this order changes. It is required
	// once an installation enables concurrent subscriptions and the customer
	// holds more than one; leaving it empty targets the primary subscription.
	SubscriptionID string
	// NewSubscription opens an additional subscription instead of changing an
	// existing one. It is only meaningful for a purchase.
	NewSubscription bool
	// SubscriptionLabel is the customer-visible name for a newly opened
	// subscription. An empty label falls back to a neutral default.
	SubscriptionLabel string
	// SelectedSquadIDs are the squads the customer chose from the plan's
	// selectable set. They are validated against the plan's squad policy.
	SelectedSquadIDs []string
	// Addons are bought together with the plan and charged on the same order,
	// so one confirmation is one payment. They cover the full first period, so
	// no proration applies to them here.
	Addons []AddonSelection
}

// Promotion and policy rejections carry a stable machine reason so a customer
// surface can explain exactly why a code was refused.
var (
	ErrPromoUnknown    = errors.New("promo_unknown")
	ErrPromoIneligible = errors.New("promo_ineligible")
	ErrPromoExhausted  = errors.New("promo_exhausted")
	ErrPromoInvalid    = errors.New("promo_invalid")
	// ErrPromoBelowCost reports a shop discount that would take the price under
	// what the provider charges. It is its own reason rather than folded into
	// ineligible, because the customer's next move differs: an ineligible code
	// is the wrong code, and this one is the right code on the wrong product.
	ErrPromoBelowCost = errors.New("promo_below_cost")
	// ErrCartMissing reports an operation on a saved cart the customer does not
	// have. It is separate from a rejection: there is nothing to reject.
	ErrCartMissing         = errors.New("cart_missing")
	ErrOperationForbidden  = errors.New("operation_forbidden")
	ErrPlanUnavailable     = errors.New("plan_unavailable")
	ErrTrialAlreadyClaimed = errors.New("trial_already_used")
	// ErrMaintenance refuses a new purchase while the installation is in
	// maintenance. Money already taken is untouched: only new orders stop.
	ErrMaintenance = errors.New("maintenance_active")
	// ErrSubscriptionUnknown is returned when a checkout names a subscription
	// that does not belong to the customer.
	ErrSubscriptionUnknown = errors.New("subscription_unknown")
	// ErrAddonUnavailable is returned when an add-on is archived, unpriced, or
	// not offered for the plan being changed.
	ErrAddonUnavailable = errors.New("addon_unavailable")
	// ErrNoActiveSubscription is returned when an add-on has nothing to attach
	// to, because the target subscription has no live entitlement.
	ErrNoActiveSubscription = errors.New("no_active_subscription")
)

// OrderQuote is the priced, validated result of evaluating a checkout intent
// against the catalog, promotions, and the customer's wallet. CreateOrder and
// PreviewOrder share it so a confirmed quote and the persisted order agree.
type OrderQuote struct {
	Plan          dbgen.GetPlanVersionForOrderRow
	DiscountMinor int64
	WalletBalance int64
	WalletMinor   int64
	ExternalMinor int64
	// SquadIDs is the resolved squad set: the plan's always-assigned squads
	// plus the customer's accepted selection.
	SquadIDs []string
	// AddonMinor is what the selected add-ons add to the order total.
	AddonMinor int64
	addons     []pricedAddon
	promo      *dbgen.GetPromoForRedemptionRow
}

// TotalMinor is what the customer owes before the wallet is applied.
func (quote OrderQuote) TotalMinor() int64 {
	return quote.Plan.AmountMinor - quote.DiscountMinor + quote.AddonMinor
}

// PreviewOrder prices a checkout intent without creating an order. It takes the
// same locks as CreateOrder and releases them by rolling back, so a preview can
// never redeem a promotion or reserve wallet credit.
func (store *Store) PreviewOrder(ctx context.Context, input CreateOrderInput) (OrderQuote, error) {
	userID, err := parseUUID(input.CustomerID)
	if err != nil {
		return OrderQuote{}, fmt.Errorf("customer ID: %w", err)
	}
	planVersionID, err := parseUUID(input.PlanVersionID)
	if err != nil {
		return OrderQuote{}, fmt.Errorf("plan version ID: %w", err)
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return OrderQuote{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return store.quote(ctx, dbgen.New(tx), userID, planVersionID, input)
}

func (store *Store) quote(ctx context.Context, queries *dbgen.Queries, userID, planVersionID pgtype.UUID, input CreateOrderInput) (OrderQuote, error) {
	plan, err := queries.GetPlanVersionForOrder(ctx, dbgen.GetPlanVersionForOrderParams{PlanVersionID: planVersionID, Currency: input.Currency})
	if errors.Is(err, pgx.ErrNoRows) {
		return OrderQuote{}, ErrPlanUnavailable
	}
	if err != nil {
		return OrderQuote{}, err
	}
	if !commerce.AllowedOperation(input.Operation, plan.UpgradePolicy, plan.DowngradePolicy) {
		return OrderQuote{}, ErrOperationForbidden
	}
	wallet, err := queries.GetAvailableWalletBalance(ctx, dbgen.GetAvailableWalletBalanceParams{TargetUserID: userID, TargetCurrency: plan.Currency})
	if err != nil {
		return OrderQuote{}, err
	}
	discount := int64(0)
	var promo *dbgen.GetPromoForRedemptionRow
	if input.PromoCode != "" {
		normalized, normalizeErr := commerce.NormalizePromoCode(input.PromoCode)
		if normalizeErr != nil {
			return OrderQuote{}, ErrPromoInvalid
		}
		lockedPromo, promoErr := queries.GetPromoForRedemption(ctx, normalized)
		if promoErr != nil {
			return OrderQuote{}, ErrPromoUnknown
		}
		eligible, eligibilityErr := queries.IsPromotionPlanEligible(ctx, dbgen.IsPromotionPlanEligibleParams{TargetPromotionID: lockedPromo.PromotionID, TargetPlanID: plan.PlanID})
		if eligibilityErr != nil || !eligible {
			return OrderQuote{}, ErrPromoIneligible
		}
		counts, countErr := queries.CountPromoRedemptions(ctx, dbgen.CountPromoRedemptionsParams{UserID: userID, PromoCodeID: lockedPromo.ID, PromotionID: lockedPromo.PromotionID})
		if countErr != nil {
			return OrderQuote{}, countErr
		}
		customerEligible, eligibilityErr := queries.CheckPromotionCustomerEligibility(ctx, dbgen.CheckPromotionCustomerEligibilityParams{Eligibility: lockedPromo.Eligibility, UserID: userID})
		if eligibilityErr != nil || !customerEligible.Valid || !customerEligible.Bool {
			return OrderQuote{}, ErrPromoIneligible
		}
		limit := lockedPromo.PromotionRedemptionLimit
		if limit.Valid && counts.TotalCount >= limit.Int32 || lockedPromo.RedemptionLimit.Valid && counts.CodeCount >= lockedPromo.RedemptionLimit.Int32 || counts.CustomerCount >= lockedPromo.PerCustomerLimit {
			return OrderQuote{}, ErrPromoExhausted
		}
		promotion := commerce.Promotion{Kind: lockedPromo.Kind, Value: lockedPromo.Value, AppliesTo: lockedPromo.AppliesTo, CustomerLimit: int(lockedPromo.PerCustomerLimit), RedemptionCount: int(counts.TotalCount), CustomerRedeemed: int(counts.CustomerCount)}
		if lockedPromo.Currency.Valid {
			promotion.Currency = lockedPromo.Currency.String
		}
		if lockedPromo.StartsAt.Valid {
			promotion.StartsAt = &lockedPromo.StartsAt.Time
		}
		if lockedPromo.EndsAt.Valid {
			promotion.EndsAt = &lockedPromo.EndsAt.Time
		}
		if limit.Valid {
			value := int(limit.Int32)
			promotion.RedemptionLimit = &value
		}
		price := commerce.Money{Amount: plan.AmountMinor, Currency: plan.Currency}
		discountMoney, discountErr := promotion.Discount(store.clock(), input.CustomerID, uuidString(plan.PlanID), price)
		if discountErr != nil {
			return OrderQuote{}, ErrPromoIneligible
		}
		discount = discountMoney.Amount
		promo = &lockedPromo
	}
	squads, err := store.resolveSquads(ctx, queries, plan, input.SelectedSquadIDs)
	if err != nil {
		return OrderQuote{}, err
	}
	// Add-ons bought with the plan cover the whole first period, so they are
	// charged at the catalog price with no proration.
	addons, addonMinor, err := store.priceInitialAddons(ctx, queries, plan, input)
	if err != nil {
		return OrderQuote{}, err
	}
	applicableWallet := wallet
	if input.SkipWallet {
		applicableWallet = 0
	}
	// The discount applies to the plan, not to the add-ons, so the add-on total
	// is added after the promotion has been taken off the plan price.
	subtotal := plan.AmountMinor + addonMinor
	domainOrder, err := commerce.NewOrder("", input.CustomerID, commerce.Money{Amount: subtotal, Currency: plan.Currency}, discount, applicableWallet)
	if err != nil {
		return OrderQuote{}, err
	}
	return OrderQuote{
		Plan: plan, DiscountMinor: discount, WalletBalance: wallet,
		WalletMinor: domainOrder.WalletMinor, ExternalMinor: domainOrder.ExternalMinor,
		SquadIDs: squads, AddonMinor: addonMinor, addons: addons, promo: promo,
	}, nil
}

// resolveSquads validates the customer's squad choice against the plan version's
// selection policy and returns the final set the entitlement will carry.
func (store *Store) resolveSquads(ctx context.Context, queries *dbgen.Queries, plan dbgen.GetPlanVersionForOrderRow, selected []string) ([]string, error) {
	policy := commerce.SquadPolicy{
		Selection: plan.SquadSelection,
		Included:  databaseutil.UUIDStrings(plan.RemnawaveSquadIds),
		Minimum:   int(plan.MinSelectableSquads),
	}
	if plan.MaxSelectableSquads.Valid {
		maximum := int(plan.MaxSelectableSquads.Int32)
		policy.Maximum = &maximum
	}
	if policy.Selection != "" && policy.Selection != "automatic" {
		offered, err := queries.ListPlanVersionSquads(ctx, plan.ID)
		if err != nil {
			return nil, err
		}
		policy.Offered = make([]string, 0, len(offered))
		for _, squad := range offered {
			policy.Offered = append(policy.Offered, uuidString(squad.SquadID))
		}
	}
	return policy.ResolveSquads(selected)
}

func (store *Store) CreateOrder(ctx context.Context, input CreateOrderInput) (dbgen.Order, error) {
	userID, err := parseUUID(input.CustomerID)
	if err != nil {
		return dbgen.Order{}, fmt.Errorf("customer ID: %w", err)
	}
	planVersionID, err := parseUUID(input.PlanVersionID)
	if err != nil {
		return dbgen.Order{}, fmt.Errorf("plan version ID: %w", err)
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return dbgen.Order{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	if existing, existingErr := queries.GetOrderByIdempotency(ctx, dbgen.GetOrderByIdempotencyParams{UserID: userID, IdempotencyKey: input.IdempotencyKey}); existingErr == nil {
		spec, specErr := queries.GetOrderEntitlementSpec(ctx, existing.ID)
		if specErr != nil {
			return dbgen.Order{}, specErr
		}
		if spec.PlanVersionID != planVersionID || existing.Currency != input.Currency || existing.Operation != input.Operation {
			return dbgen.Order{}, errors.New("idempotency key was already used with different order parameters")
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return dbgen.Order{}, commitErr
		}
		return existing, nil
	} else if !errors.Is(existingErr, pgx.ErrNoRows) {
		return dbgen.Order{}, existingErr
	}
	if err = store.assertOperational(ctx, queries); err != nil {
		return dbgen.Order{}, err
	}
	quote, err := store.quote(ctx, queries, userID, planVersionID, input)
	if err != nil {
		return dbgen.Order{}, err
	}
	plan, discount, promo := quote.Plan, quote.DiscountMinor, quote.promo
	subscriptionID, err := store.resolveSubscription(ctx, queries, userID, plan, input)
	if err != nil {
		return dbgen.Order{}, err
	}
	squadIDs, err := databaseutil.ParseUUIDs(quote.SquadIDs)
	if err != nil {
		return dbgen.Order{}, err
	}
	domainOrder := commerce.Order{WalletMinor: quote.WalletMinor, ExternalMinor: quote.ExternalMinor}
	state := string(commerce.OrderPending)
	if domainOrder.ExternalMinor == 0 {
		state = string(commerce.OrderPaid)
	}
	expiresAt := store.paymentDeadline(input.ExpiresAt, input.Provider)
	order, err := queries.CreateOrder(ctx, dbgen.CreateOrderParams{
		UserID: userID, State: state, Operation: input.Operation, Currency: plan.Currency,
		SubtotalMinor: plan.AmountMinor + quote.AddonMinor, DiscountMinor: discount,
		WalletMinor:   domainOrder.WalletMinor,
		ExternalMinor: domainOrder.ExternalMinor, IdempotencyKey: input.IdempotencyKey,
		ExpiresAt:      pgtype.Timestamptz{Time: expiresAt, Valid: true},
		SubscriptionID: subscriptionID, SelectedSquadIds: squadIDs,
	})
	if err != nil {
		return dbgen.Order{}, err
	}
	// Add-ons bought with the plan are charged on this order, so one
	// confirmation is one payment rather than two.
	if err = store.insertAddonLines(ctx, queries, order.ID, quote.addons, expiresAt); err != nil {
		return dbgen.Order{}, err
	}
	snapshot, _ := json.Marshal(map[string]any{
		"planCode": plan.Code, "planKind": plan.Kind, "version": plan.Version,
		"billingPeriod": plan.BillingPeriod, "durationSeconds": plan.DurationSeconds,
		"trafficAllowanceBytes": nullableInt8(plan.TrafficAllowanceBytes),
		"deviceLimit":           nullableInt4(plan.DeviceLimit), "squadIds": quote.SquadIDs,
		"planSquadIds":  databaseutil.UUIDStrings(plan.RemnawaveSquadIds),
		"upgradePolicy": plan.UpgradePolicy, "downgradePolicy": plan.DowngradePolicy,
		"cancellationPolicy": plan.CancellationPolicy,
	})
	if _, err = queries.InsertOrderLine(ctx, dbgen.InsertOrderLineParams{OrderID: order.ID, PlanID: plan.PlanID, PlanVersionID: plan.ID, UnitAmountMinor: plan.AmountMinor, Snapshot: snapshot}); err != nil {
		return dbgen.Order{}, err
	}
	if promo != nil {
		if _, err = queries.InsertPromoRedemption(ctx, dbgen.InsertPromoRedemptionParams{PromoCodeID: promo.ID, PromotionID: promo.PromotionID, UserID: userID, OrderID: order.ID, DiscountMinor: discount}); err != nil {
			return dbgen.Order{}, err
		}
	}
	if plan.Kind == "trial" {
		// A trial is claimed in the same transaction as the order it belongs to,
		// so a duplicate tap or a concurrent update can never mint a second one.
		// A claim whose order closed unpaid is a claim that never happened, so
		// it is taken over rather than refused; a claim behind a live or paid
		// order stands.
		claim, claimErr := tx.Exec(ctx, `INSERT INTO trial_claims (user_id, plan_id, order_id)
			VALUES ($1, $2, $3)
			ON CONFLICT (user_id) DO UPDATE
			SET plan_id = EXCLUDED.plan_id, order_id = EXCLUDED.order_id, claimed_at = now()
			WHERE EXISTS (SELECT 1 FROM orders closed
				WHERE closed.id = trial_claims.order_id AND closed.state IN ('cancelled', 'expired'))`,
			userID, plan.PlanID, order.ID)
		if claimErr != nil {
			return dbgen.Order{}, claimErr
		}
		if claim.RowsAffected() == 0 {
			return dbgen.Order{}, ErrTrialAlreadyClaimed
		}
	}
	if state == string(commerce.OrderPaid) {
		if err = store.settlePaidOrder(ctx, tx, queries, order, "wallet:"+input.IdempotencyKey); err != nil {
			return dbgen.Order{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return dbgen.Order{}, err
	}
	return order, nil
}

// paymentDeadline is when an order's payment window closes: the explicit
// instant a caller supplied, or now plus the window the provider warrants.
func (store *Store) paymentDeadline(explicit time.Time, provider string) time.Time {
	if !explicit.IsZero() {
		return explicit
	}
	return store.clock().Add(commerce.PaymentWindow(provider))
}

// ExtendOrderExpiry moves a pending order's payment window out to `until`, for
// a provider chosen after the order was created. It only ever lengthens the
// window — an order already open for longer keeps its deadline — and it
// refuses an order that is not pending, so a settled, cancelled, or already
// expired order is never reopened. A goods order is left alone: its deadline
// is the gateway quote's validity, and a stale quote must not be paid.
func (store *Store) ExtendOrderExpiry(ctx context.Context, orderID string, until time.Time) error {
	id, err := parseUUID(orderID)
	if err != nil {
		return err
	}
	_, err = dbgen.New(store.pool).ExtendOrderExpiry(ctx, dbgen.ExtendOrderExpiryParams{OrderID: id, ExpiresAt: pgtype.Timestamptz{Time: until, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		// Not pending, or a goods order: nothing to extend, and nothing wrong.
		return nil
	}
	return err
}

func (store *Store) RecordProviderPayment(ctx context.Context, paymentIntentID, providerEventID string, amount commerce.Money, late bool) (dbgen.Order, string, error) {
	intentID, err := parseUUID(paymentIntentID)
	if err != nil {
		return dbgen.Order{}, "", err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return dbgen.Order{}, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	intent, err := queries.GetPaymentIntent(ctx, intentID)
	if err != nil {
		return dbgen.Order{}, "", err
	}
	order, err := queries.LockOrder(ctx, intent.OrderID)
	if err != nil {
		return dbgen.Order{}, "", err
	}
	domainOrder := commerce.Order{ID: uuidString(order.ID), CustomerID: uuidString(order.UserID), State: commerce.OrderState(order.State), Subtotal: commerce.Money{Amount: order.SubtotalMinor, Currency: order.Currency}, DiscountMinor: order.DiscountMinor, WalletMinor: order.WalletMinor, ExternalMinor: order.ExternalMinor, PaidMinor: order.PaidMinor, RefundedMinor: order.RefundedMinor}
	updated, classification, applyErr := domainOrder.ApplyPayment(commerce.PaymentResult{Amount: amount, Late: late})
	// A wallet top-up owes nothing beyond the money itself, so a mismatch is
	// resolved by crediting exactly what arrived instead of parking the order in
	// a state that contradicts the payment.
	if applyErr == nil && order.Operation == "topup" && (classification == "overpayment" || classification == "underpayment") {
		updated.PaidMinor, updated.State = amount.Amount, commerce.OrderPaid
	}
	if applyErr == nil && updated.State == commerce.OrderPaid && order.WalletMinor > 0 {
		walletBalance, balanceErr := queries.GetWalletBalance(ctx, dbgen.GetWalletBalanceParams{UserID: order.UserID, Currency: order.Currency})
		if balanceErr != nil {
			return dbgen.Order{}, "", balanceErr
		}
		if walletBalance < order.WalletMinor {
			classification = "wallet_unavailable"
		}
	}
	// payment_events.type is a closed set. A payment after cancellation is
	// recorded as the late settlement it is, with the classification and the
	// order's previous state in the details so the timeline still says what
	// happened.
	eventType := paymentEventType(classification)
	details, _ := json.Marshal(map[string]any{"classification": classification, "previousState": order.State})
	if _, err = queries.InsertPaymentEvent(ctx, dbgen.InsertPaymentEventParams{PaymentIntentID: intent.ID, Type: eventType, PreviousStatus: pgtype.Text{String: intent.Status, Valid: true}, Status: pgtype.Text{String: "succeeded", Valid: true}, AmountMinor: pgtype.Int8{Int64: amount.Amount, Valid: true}, Currency: pgtype.Text{String: amount.Currency, Valid: true}, ProviderEventID: pgtype.Text{String: providerEventID, Valid: true}, Details: details}); err != nil {
		return dbgen.Order{}, "", err
	}
	settles := updated.State == commerce.OrderPaid
	if applyErr != nil || !settles || classification == commerce.ClassificationCurrency || classification == commerce.ClassificationWallet || classification == commerce.ClassificationDuplicate {
		// Money arrived and the order did not settle. Nothing automatic
		// resolves that, so the operator is told inside the same commit.
		if commerce.NeedsOperator(classification) {
			store.notifyPaymentAttention(ctx, queries, intent, order, amount, classification)
		}
		if err = tx.Commit(ctx); err != nil {
			return dbgen.Order{}, "", err
		}
		return order, classification, applyErr
	}
	if classification == commerce.ClassificationPaidAfterCancellation {
		// It settles — the customer paid — but they had been told nothing
		// would be charged, so an operator sees it rather than discovering it
		// from a complaint.
		store.notifyPaymentAttention(ctx, queries, intent, order, amount, classification)
	}
	if _, err = queries.UpdatePaymentIntentStatus(ctx, dbgen.UpdatePaymentIntentStatusParams{PaymentIntentID: intent.ID, Status: "succeeded"}); err != nil {
		return dbgen.Order{}, "", err
	}
	order, err = queries.UpdateOrderPayment(ctx, dbgen.UpdateOrderPaymentParams{OrderID: order.ID, State: string(updated.State), PaidMinor: updated.PaidMinor})
	if err != nil {
		return dbgen.Order{}, "", err
	}
	if err = store.settlePaidOrder(ctx, tx, queries, order, "payment:"+providerEventID); err != nil {
		return dbgen.Order{}, "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return dbgen.Order{}, "", err
	}
	// A settled top-up releases a saved cart. This runs after the commit on
	// purpose: a cart failure must never roll back a payment that was received.
	if order.Operation == "topup" {
		if _, cartErr := store.TryPurchaseCart(context.WithoutCancel(ctx), uuidString(order.UserID)); cartErr != nil {
			store.options.Logger.Warn("deferred cart purchase after top-up failed", "error", cartErr, "order_id", uuidString(order.ID))
		}
	}
	return order, classification, nil
}

// paymentEventType maps a classification onto the closed set of
// payment_events.type values.
func paymentEventType(classification string) string {
	switch classification {
	case commerce.ClassificationPaid:
		return "status_changed"
	case commerce.ClassificationCurrency:
		return "amount_mismatch"
	case commerce.ClassificationPaidAfterCancellation:
		return "late"
	default:
		return classification
	}
}

// notifyPaymentAttention queues an incident notice for a payment an operator
// has to look at. It carries identifiers, the classification, and the amount
// class — never a provider payload or customer content — and is deduplicated
// per intent and classification, so a replayed webhook adds nothing.
func (store *Store) notifyPaymentAttention(ctx context.Context, queries *dbgen.Queries, intent dbgen.PaymentIntent, order dbgen.Order, amount commerce.Money, classification string) {
	store.notifyOperator(ctx, queries, "incident", "payment:"+uuidString(intent.ID)+":"+classification, map[string]any{
		"orderId": uuidString(order.ID), "provider": intent.Provider, "operation": order.Operation,
		"classification": classification, "currency": amount.Currency, "amountMinor": amount.Amount,
		"status": "payment_needs_attention", "reason": "order_" + order.State,
	})
}

func (store *Store) RecordRefund(ctx context.Context, paymentIntentID, refundID, idempotencyKey string, amount commerce.Money) (dbgen.Order, error) {
	intentID, err := parseUUID(paymentIntentID)
	if err != nil {
		return dbgen.Order{}, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return dbgen.Order{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	intent, err := queries.GetPaymentIntent(ctx, intentID)
	if err != nil {
		return dbgen.Order{}, err
	}
	order, err := queries.LockOrder(ctx, intent.OrderID)
	if err != nil {
		return dbgen.Order{}, err
	}
	if _, existingErr := queries.GetLedgerTransactionByIdempotency(ctx, idempotencyKey); existingErr == nil {
		return order, tx.Commit(ctx)
	} else if !errors.Is(existingErr, pgx.ErrNoRows) {
		return dbgen.Order{}, existingErr
	}
	if amount.Currency != order.Currency || amount.Amount <= 0 || order.RefundedMinor+amount.Amount > order.ExternalMinor {
		return dbgen.Order{}, errors.New("refund exceeds paid amount")
	}
	refunded := order.RefundedMinor + amount.Amount
	state := string(commerce.OrderPartiallyRefunded)
	if refunded == order.PaidMinor {
		state = string(commerce.OrderRefunded)
	}
	transaction, err := queries.CreateLedgerTransaction(ctx, dbgen.CreateLedgerTransactionParams{Type: "refund", ReferenceType: "refund", ReferenceID: refundID, IdempotencyKey: idempotencyKey})
	if err != nil {
		return dbgen.Order{}, err
	}
	if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "platform_clearing", Currency: amount.Currency, AmountMinor: -amount.Amount}); err != nil {
		return dbgen.Order{}, err
	}
	if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "provider_clearing", Currency: amount.Currency, AmountMinor: amount.Amount}); err != nil {
		return dbgen.Order{}, err
	}
	order, err = queries.UpdateOrderRefund(ctx, dbgen.UpdateOrderRefundParams{OrderID: order.ID, State: state, RefundedMinor: refunded})
	if err != nil {
		return dbgen.Order{}, err
	}
	store.notifyOperator(ctx, queries, "refund", "refund:"+refundID, map[string]any{
		"orderId": uuidString(order.ID), "currency": amount.Currency,
		"amountMinor": amount.Amount, "status": state,
	})
	return order, tx.Commit(ctx)
}

func (store *Store) AdjustWallet(ctx context.Context, customerID, currency string, amount int64, kind, reference, idempotencyKey, reason, actorID string) error {
	if reason == "" || actorID == "" || amount == 0 {
		return errors.New("operator adjustment requires amount, reason, and actor")
	}
	if kind != "credit" && kind != "debit" && kind != "referral_reward" && kind != "correction" && kind != "expiration" {
		return errors.New("unsupported wallet adjustment type")
	}
	if (kind == "credit" || kind == "referral_reward") && amount < 0 || (kind == "debit" || kind == "expiration") && amount > 0 {
		return errors.New("wallet adjustment amount has the wrong sign for its type")
	}
	userID, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	entries := []commerce.LedgerEntry{
		{AccountType: "customer_wallet", CustomerID: customerID, Currency: currency, AmountMinor: amount},
		{AccountType: "platform_clearing", Currency: currency, AmountMinor: -amount},
	}
	if err := commerce.ValidateLedger(entries); err != nil {
		return err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	if _, existingErr := queries.GetLedgerTransactionByIdempotency(ctx, idempotencyKey); existingErr == nil {
		existing, entryErr := queries.GetLedgerTransactionByIdempotency(ctx, idempotencyKey)
		if entryErr != nil || existing.Type != kind || existing.ReferenceID != reference || existing.ActorID.String != actorID {
			return errors.New("idempotency key was already used with different wallet parameters")
		}
		entries, entryErr := queries.ListLedgerEntriesByTransaction(ctx, existing.ID)
		if entryErr != nil {
			return entryErr
		}
		matched := false
		for _, entry := range entries {
			if entry.AccountType == "customer_wallet" && entry.UserID == userID && entry.Currency == currency && entry.AmountMinor == amount {
				matched = true
			}
		}
		if !matched {
			return errors.New("idempotency key was already used with different wallet parameters")
		}
		return tx.Commit(ctx)
	} else if !errors.Is(existingErr, pgx.ErrNoRows) {
		return existingErr
	}
	if amount < 0 {
		balance, balanceErr := queries.GetWalletBalance(ctx, dbgen.GetWalletBalanceParams{UserID: userID, Currency: currency})
		if balanceErr != nil {
			return balanceErr
		}
		if balance+amount < 0 {
			return errors.New("wallet adjustment would create a negative balance")
		}
	}
	transaction, err := queries.CreateLedgerTransaction(ctx, dbgen.CreateLedgerTransactionParams{Type: kind, ReferenceType: "operator_adjustment", ReferenceID: reference, IdempotencyKey: idempotencyKey, Reason: pgtype.Text{String: reason, Valid: true}, ActorID: pgtype.Text{String: actorID, Valid: true}})
	if err != nil {
		return err
	}
	if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "customer_wallet", UserID: userID, Currency: currency, AmountMinor: amount}); err != nil {
		return err
	}
	if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "platform_clearing", Currency: currency, AmountMinor: -amount}); err != nil {
		return err
	}
	metadata, _ := json.Marshal(map[string]any{"amountMinor": amount, "currency": currency})
	if _, err = queries.InsertAuditEvent(ctx, dbgen.InsertAuditEventParams{ActorType: "operator", ActorID: pgtype.Text{String: actorID, Valid: true}, Action: "wallet.adjusted", Category: "financial", Outcome: "success", TargetType: "customer", TargetID: customerID, Reason: pgtype.Text{String: reason, Valid: true}, Metadata: metadata}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// settlePaidOrder turns a paid order into the state it bought. It runs inside
// the transaction that recorded the payment, so the ledger movement, the
// entitlement, and the durable fulfillment job all commit together or not at
// all.
func (store *Store) settlePaidOrder(ctx context.Context, tx pgx.Tx, queries *dbgen.Queries, order dbgen.Order, idempotencyKey string) error {
	switch order.Operation {
	case "topup":
		// A top-up buys nothing but credit, so it never creates an entitlement
		// and never qualifies a referral.
		return store.creditTopUp(ctx, queries, order, idempotencyKey)
	case "addon":
		if err := store.recordOrderRevenue(ctx, queries, order, idempotencyKey); err != nil {
			return err
		}
		return store.applyOrderAddons(ctx, tx, queries, order, idempotencyKey)
	case "goods":
		// A shop purchase is not a subscription: it creates a delivery, not an
		// entitlement, and nothing is pushed to Remnawave.
		return store.settleGoodsOrder(ctx, queries, order, idempotencyKey)
	case "gift":
		// A gift creates nothing for the buyer. What it produces belongs to
		// whoever claims the code, and it is produced then.
		return store.settleGiftOrder(ctx, queries, order, idempotencyKey)
	}
	if err := store.recordOrderRevenue(ctx, queries, order, idempotencyKey); err != nil {
		return err
	}
	spec, err := queries.GetOrderEntitlementSpec(ctx, order.ID)
	if err != nil {
		return err
	}
	startsAt := store.clock().UTC()
	currentEndsAt, err := store.changeBase(ctx, queries, order.UserID, order.SubscriptionID, startsAt)
	if err != nil {
		return err
	}
	schedule, err := commerce.ScheduleEntitlement(startsAt, time.Duration(spec.DurationSeconds)*time.Second, order.Operation, spec.UpgradePolicy, spec.DowngradePolicy, currentEndsAt)
	if err != nil {
		return err
	}
	// The order carries the squad set the customer confirmed. A plan whose
	// squads are assigned automatically leaves it empty and falls back to the
	// plan version's own set.
	squadIDs := order.SelectedSquadIds
	if len(squadIDs) == 0 {
		squadIDs = spec.RemnawaveSquadIds
	}
	entitlement, err := queries.CreateEntitlement(ctx, dbgen.CreateEntitlementParams{UserID: order.UserID, OrderID: order.ID, PlanVersionID: spec.PlanVersionID, StartsAt: pgtype.Timestamptz{Time: schedule.StartsAt, Valid: true}, EndsAt: pgtype.Timestamptz{Time: schedule.EndsAt, Valid: true}, TrafficAllowanceBytes: spec.TrafficAllowanceBytes, DeviceLimit: spec.DeviceLimit, RemnawaveSquadIds: squadIDs, SubscriptionID: order.SubscriptionID})
	if err != nil {
		return err
	}
	// Add-ons bought with the plan are folded into the entitlement before the
	// desired state is computed, so provisioning pushes one combined result.
	if entitlement, err = store.applyAddonLines(ctx, queries, order, entitlement); err != nil {
		return err
	}
	desired, _ := json.Marshal(map[string]any{"effectiveAt": schedule.EffectiveAt, "endsAt": schedule.EndsAt, "trafficAllowanceBytes": nullableInt8(entitlement.TrafficAllowanceBytes), "deviceLimit": nullableInt4(entitlement.DeviceLimit), "squadIds": databaseutil.UUIDStrings(entitlement.RemnawaveSquadIds), "resetTraffic": commerce.ResetsTraffic(order.Operation)})
	operation, err := queries.CreateFulfillmentOperation(ctx, dbgen.CreateFulfillmentOperationParams{EntitlementID: entitlement.ID, Operation: "create", IdempotencyKey: "order:" + uuidString(order.ID) + ":fulfill", CorrelationID: idempotencyKey, DesiredState: desired})
	if err != nil {
		return err
	}
	if _, err = queries.InsertOutboxEvent(ctx, dbgen.InsertOutboxEventParams{Topic: "order.paid", Payload: []byte(fmt.Sprintf(`{"orderId":%q,"entitlementId":%q}`, uuidString(order.ID), uuidString(entitlement.ID)))}); err != nil {
		return err
	}
	store.notifyOperator(ctx, queries, operatorKindForOrder(order.Operation), "order:"+uuidString(order.ID), map[string]any{
		"orderId": uuidString(order.ID), "subscriptionId": uuidStringOrEmpty(order.SubscriptionID),
		"operation": order.Operation, "currency": order.Currency, "amountMinor": order.PaidMinor,
	})
	// Referral rewards are granted in the same transaction as the settlement
	// that qualified them, so a retried webhook can never credit twice.
	program, err := store.referralProgram(ctx, tx)
	if err != nil {
		return err
	}
	if err = store.grantReferralRewards(ctx, tx, queries, order, program); err != nil {
		return err
	}
	if store.enqueue != nil {
		if err = store.enqueue(ctx, tx, uuidString(operation.ID)); err != nil {
			return err
		}
	}
	return nil
}

// changeBase is the end the subscription's current entitlement gives a change
// to build on, or nil when there is none. A paused entitlement contributes the
// time it was preserving, measured from now, so an extension bought while
// paused resumes the subscription with exactly those days plus the new period;
// the paused row is then superseded by the new entitlement's provisioning.
func (store *Store) changeBase(ctx context.Context, queries *dbgen.Queries, userID, subscriptionID pgtype.UUID, now time.Time) (*time.Time, error) {
	current, err := queries.GetLatestEntitlementForChange(ctx, dbgen.GetLatestEntitlementForChangeParams{UserID: userID, SubscriptionID: subscriptionID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	base := commerce.EffectiveEndsAt(now, current.EndsAt.Time, current.PausedAt.Time)
	return &base, nil
}

func parseUUID(value string) (pgtype.UUID, error) {
	var result pgtype.UUID
	if err := result.Scan(value); err != nil {
		return pgtype.UUID{}, err
	}
	if !result.Valid {
		return pgtype.UUID{}, errors.New("invalid UUID")
	}
	return result, nil
}

func uuidString(value pgtype.UUID) string {
	b := value.Bytes
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func nullableInt8(value pgtype.Int8) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func nullableInt4(value pgtype.Int4) any {
	if !value.Valid {
		return nil
	}
	return value.Int32
}
