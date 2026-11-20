package commercepg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// CreditAddonRemainder is the unused remainder of add-on packs a capacity
// change releases.
const CreditAddonRemainder = "addon_remainder"

// Order operations that change a live subscription's capacity part-way
// through its period.
const (
	OperationTrafficChange = "traffic_change"
)

// capacityChange is a priced mid-period change of one subscription's
// capacity: the packs it releases, what is left of them, and the pack it
// adds.
type capacityChange struct {
	operation   string
	entitlement dbgen.Entitlement
	released    []releasedPack
	creditMinor int64
	basisMinor  int64
	target      *pricedAddon
	chargeMinor int64
	// currentTraffic and targetTraffic are the traffic packs' bytes before
	// and after the change.
	currentTraffic, targetTraffic int64
}

type releasedPack struct {
	pack   dbgen.ListLivePacksRow
	credit commerce.RemainderCredit
}

// CapacityChangeQuote is what a capacity change costs or hands back now.
type CapacityChangeQuote struct {
	Operation string
	Currency  string
	// ChargeMinor is the prorated price of what is added, CreditMinor the
	// prorated value of what is released. CreditAppliedMinor is the part of
	// the credit the order spends, CreditToWalletMinor the part that stays in
	// the wallet. WalletMinor is the customer's own balance the order spends
	// and ExternalMinor what a provider must settle.
	ChargeMinor         int64
	CreditMinor         int64
	CreditAppliedMinor  int64
	CreditToWalletMinor int64
	WalletMinor         int64
	ExternalMinor       int64
	WalletBalanceMinor  int64
	// CurrentTrafficBytes and TargetTrafficBytes are a traffic package
	// change's packs before and after.
	CurrentTrafficBytes int64
	TargetTrafficBytes  int64
	ReleasedPacks       int
	EndsAt              time.Time
}

// CapacityChangeInput is a customer's mid-period capacity change.
type CapacityChangeInput struct {
	CustomerID     string
	SubscriptionID string
	Currency       string
	IdempotencyKey string
	// AddonVersionID is the traffic package to switch to; empty releases
	// the current packages and adds none.
	AddonVersionID string
	SkipWallet     bool
	ExpiresAt      time.Time
	Provider       string
}

// liveForChange resolves the customer's subscription and its live, running
// entitlement. A paused entitlement's clock is stopped, so it has no
// remaining period to price a change against.
func (store *Store) liveForChange(ctx context.Context, queries *dbgen.Queries, userID pgtype.UUID, subscriptionID string) (dbgen.Entitlement, error) {
	target, err := parseUUID(subscriptionID)
	if err != nil {
		return dbgen.Entitlement{}, ErrSubscriptionUnknown
	}
	subscription, err := queries.GetCustomerSubscription(ctx, dbgen.GetCustomerSubscriptionParams{SubscriptionID: target, UserID: userID})
	if err != nil || subscription.Status != "active" {
		return dbgen.Entitlement{}, ErrSubscriptionUnknown
	}
	entitlement, err := queries.GetLatestEntitlementForChange(ctx, dbgen.GetLatestEntitlementForChangeParams{UserID: userID, SubscriptionID: target})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.Entitlement{}, ErrNoActiveSubscription
	}
	if err != nil {
		return dbgen.Entitlement{}, err
	}
	now := store.clock()
	if entitlement.Status == "paused" || !commerce.EntitlementLive(entitlement.Status, entitlement.EndsAt.Time, now) || entitlement.StartsAt.Time.After(now) {
		return dbgen.Entitlement{}, ErrNoActiveSubscription
	}
	return entitlement, nil
}

// packCredit prices what is left of one pack at what its order took for it.
// A pack another customer paid for (a gift) or one bought in another currency
// is released with nothing handed back.
func packCredit(pack dbgen.ListLivePacksRow, entitlement dbgen.Entitlement, currency string, now time.Time) commerce.RemainderCredit {
	end := entitlement.EndsAt.Time
	if pack.ExpiresAt.Valid && pack.ExpiresAt.Time.Before(end) {
		end = pack.ExpiresAt.Time
	}
	if pack.OrderUserID != entitlement.UserID || pack.OrderCurrency != currency {
		return commerce.RemainderCredit{Period: end.Sub(pack.AppliedAt.Time)}
	}
	basis := commerce.PackBasis(pack.ChargedMinor, pack.OrderPaidMinor, pack.OrderSubtotalMinor)
	return commerce.CreditPack(basis, now, pack.AppliedAt.Time, end)
}

// release adds a pack to the change and its remainder to the credit.
func (change *capacityChange) release(pack dbgen.ListLivePacksRow, credit commerce.RemainderCredit) {
	change.released = append(change.released, releasedPack{pack: pack, credit: credit})
	change.creditMinor += credit.CreditMinor
	change.basisMinor += credit.BasisMinor
}

// planTrafficChange prices a switch of the subscription's traffic packages to
// one other package, or to none.
func (store *Store) planTrafficChange(ctx context.Context, queries *dbgen.Queries, userID pgtype.UUID, input CapacityChangeInput) (capacityChange, error) {
	entitlement, err := store.liveForChange(ctx, queries, userID, input.SubscriptionID)
	if err != nil {
		return capacityChange{}, err
	}
	if !entitlement.TrafficAllowanceBytes.Valid {
		return capacityChange{}, commerce.ErrTrafficChangeUnavailable
	}
	packs, err := queries.ListLivePacks(ctx, entitlement.ID)
	if err != nil {
		return capacityChange{}, err
	}
	now := store.clock().UTC()
	change := capacityChange{operation: OperationTrafficChange, entitlement: entitlement}
	for _, pack := range packs {
		if pack.AddonKind != "traffic" || !pack.TrafficBytes.Valid || pack.TrafficBytes.Int64 <= 0 {
			continue
		}
		change.currentTraffic += pack.TrafficBytes.Int64
		change.release(pack, packCredit(pack, entitlement, input.Currency, now))
	}
	if input.AddonVersionID != "" {
		row, rowErr := store.addonVersionForPlan(ctx, queries, entitlement.PlanVersionID, input.Currency, input.AddonVersionID, map[string]struct{}{},
			addonTarget{traffic: true, devices: entitlement.DeviceLimit.Valid})
		if rowErr != nil {
			return capacityChange{}, rowErr
		}
		if row.Kind != "traffic" || !row.TrafficBytes.Valid {
			return capacityChange{}, ErrAddonUnavailable
		}
		charge, priceErr := commerce.PriceAddon(row.AmountMinor, 1, int(row.MaxQuantity), commerce.Proration(row.Proration), now, entitlement.StartsAt.Time, entitlement.EndsAt.Time)
		if priceErr != nil {
			return capacityChange{}, priceErr
		}
		change.target = &pricedAddon{row: row, charge: charge}
		change.chargeMinor = charge.ChargedMinor
		change.targetTraffic = row.TrafficBytes.Int64
	}
	if change.targetTraffic == change.currentTraffic {
		return capacityChange{}, commerce.ErrCapacityChangeSame
	}
	// The cap on bought traffic counts what stays plus what is added.
	bounds, err := queries.GetAddonLimits(ctx, entitlement.ID)
	if err != nil {
		return capacityChange{}, err
	}
	limits := commerce.AddonLimits{
		MaxBoughtTraffic: optionalInt64(bounds.MaxAddonTrafficBytes),
		BoughtTraffic:    max(bounds.BoughtTrafficBytes-change.currentTraffic, 0),
	}
	if err = commerce.CheckAddonLimits(limits, nil, 0, change.targetTraffic); err != nil {
		return capacityChange{}, fmt.Errorf("%w: %w", ErrAddonUnavailable, err)
	}
	return change, nil
}

// quoteChange applies the wallet and the credit to a priced change.
func quoteChange(change capacityChange, currency string, walletBalance int64, skipWallet bool) (CapacityChangeQuote, commerce.Order, error) {
	usable := walletBalance
	if skipWallet {
		usable = 0
	}
	domainOrder, err := commerce.NewOrder("", "", commerce.Money{Amount: change.chargeMinor, Currency: currency}, 0, usable+change.creditMinor)
	if err != nil {
		return CapacityChangeQuote{}, commerce.Order{}, err
	}
	applied, toWallet := commerce.ApplyCredit(change.creditMinor, change.chargeMinor)
	return CapacityChangeQuote{
		Operation: change.operation, Currency: currency,
		ChargeMinor: change.chargeMinor, CreditMinor: change.creditMinor,
		CreditAppliedMinor: applied, CreditToWalletMinor: toWallet,
		WalletMinor: domainOrder.WalletMinor - applied, ExternalMinor: domainOrder.ExternalMinor,
		WalletBalanceMinor:  walletBalance,
		CurrentTrafficBytes: change.currentTraffic, TargetTrafficBytes: change.targetTraffic,
		ReleasedPacks: len(change.released), EndsAt: change.entitlement.EndsAt.Time,
	}, domainOrder, nil
}

// TrafficChangeQuote prices switching a subscription's traffic package to the
// given add-on version, or to none when it is empty, without opening an order.
func (store *Store) TrafficChangeQuote(ctx context.Context, input CapacityChangeInput) (CapacityChangeQuote, error) {
	userID, err := parseUUID(input.CustomerID)
	if err != nil {
		return CapacityChangeQuote{}, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return CapacityChangeQuote{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	change, err := store.planTrafficChange(ctx, queries, userID, input)
	if err != nil {
		return CapacityChangeQuote{}, err
	}
	wallet, err := availableWallet(ctx, queries, userID, input.Currency)
	if err != nil {
		return CapacityChangeQuote{}, err
	}
	quote, _, err := quoteChange(change, input.Currency, wallet, input.SkipWallet)
	return quote, err
}

// CreateTrafficChangeOrder opens the order that switches a subscription's
// traffic package. A bigger package is charged its prorated price less what
// is left of the packages it replaces; a smaller one costs less than that
// remainder, and the difference is credited to the wallet. Either way it is an
// ordinary order with one credit line, settled by the wallet when it covers
// it and by a provider otherwise.
func (store *Store) CreateTrafficChangeOrder(ctx context.Context, input CapacityChangeInput) (dbgen.Order, error) {
	return store.createCapacityChangeOrder(ctx, input, OperationTrafficChange, store.planTrafficChange)
}

func (store *Store) createCapacityChangeOrder(
	ctx context.Context, input CapacityChangeInput, operation string,
	plan func(context.Context, *dbgen.Queries, pgtype.UUID, CapacityChangeInput) (capacityChange, error),
) (dbgen.Order, error) {
	userID, err := parseUUID(input.CustomerID)
	if err != nil {
		return dbgen.Order{}, fmt.Errorf("customer ID: %w", err)
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return dbgen.Order{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	if existing, existingErr := queries.GetOrderByIdempotency(ctx, dbgen.GetOrderByIdempotencyParams{UserID: userID, IdempotencyKey: input.IdempotencyKey}); existingErr == nil {
		if existing.Operation != operation || existing.Currency != input.Currency {
			return dbgen.Order{}, errors.New("idempotency key was already used with different parameters")
		}
		return existing, tx.Commit(ctx)
	} else if !errors.Is(existingErr, pgx.ErrNoRows) {
		return dbgen.Order{}, existingErr
	}
	if err = store.assertNotRestricted(ctx, queries, userID, "purchase"); err != nil {
		return dbgen.Order{}, err
	}
	if err = store.assertOperational(ctx, queries); err != nil {
		return dbgen.Order{}, err
	}
	change, err := plan(ctx, queries, userID, input)
	if err != nil {
		return dbgen.Order{}, err
	}
	wallet, err := availableWallet(ctx, queries, userID, input.Currency)
	if err != nil {
		return dbgen.Order{}, err
	}
	_, domainOrder, err := quoteChange(change, input.Currency, wallet, input.SkipWallet)
	if err != nil {
		return dbgen.Order{}, err
	}
	state := string(commerce.OrderPending)
	if domainOrder.ExternalMinor == 0 {
		state = string(commerce.OrderPaid)
	}
	expiresAt := store.paymentDeadline(input.ExpiresAt, input.Provider)
	order, err := queries.CreateOrder(ctx, dbgen.CreateOrderParams{
		UserID: userID, State: state, Operation: operation, Currency: input.Currency,
		SubtotalMinor: change.chargeMinor, WalletMinor: domainOrder.WalletMinor, ExternalMinor: domainOrder.ExternalMinor,
		IdempotencyKey: input.IdempotencyKey, SubscriptionID: change.entitlement.SubscriptionID,
		ExpiresAt:        pgtype.Timestamptz{Time: expiresAt, Valid: true},
		SelectedSquadIds: noSquads(),
	})
	if err != nil {
		return dbgen.Order{}, err
	}
	if change.target != nil {
		if err = store.insertAddonLines(ctx, queries, order.ID, []pricedAddon{*change.target}, change.entitlement.EndsAt.Time); err != nil {
			return dbgen.Order{}, err
		}
	}
	released := make([]pgtype.UUID, 0, len(change.released))
	for _, item := range change.released {
		released = append(released, item.pack.ID)
	}
	now := store.clock().UTC()
	if _, err = queries.InsertOrderCredit(ctx, dbgen.InsertOrderCreditParams{
		OrderID: order.ID, UserID: userID, Kind: CreditAddonRemainder,
		SourceEntitlementID: change.entitlement.ID, Currency: input.Currency,
		BasisMinor: change.basisMinor, CreditMinor: change.creditMinor,
		RemainingSeconds: max(int64(change.entitlement.EndsAt.Time.Sub(now)/time.Second), 0),
		PeriodSeconds:    max(int64(change.entitlement.EndsAt.Time.Sub(change.entitlement.StartsAt.Time)/time.Second), 1),
		ReleasedAddonIds: released,
	}); err != nil {
		return dbgen.Order{}, err
	}
	if state == string(commerce.OrderPaid) {
		if err = store.settlePaidOrder(ctx, tx, queries, order, "wallet:"+input.IdempotencyKey); err != nil {
			return dbgen.Order{}, err
		}
	}
	return order, tx.Commit(ctx)
}

// capacityCreditOwed re-checks a capacity change's credit at settlement:
// every pack it hands back must still be counting on the subscription's
// current entitlement. A pack another change released first, or one whose
// lifetime ran out, was already taken back and is not paid for twice.
func (store *Store) capacityCreditOwed(ctx context.Context, queries *dbgen.Queries, order dbgen.Order, line dbgen.OrderCredit) (bool, error) {
	current, err := queries.GetLatestEntitlementForChange(ctx, dbgen.GetLatestEntitlementForChangeParams{UserID: order.UserID, SubscriptionID: order.SubscriptionID})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil || current.ID != line.SourceEntitlementID {
		return false, err
	}
	count, err := queries.CountReleasablePacks(ctx, dbgen.CountReleasablePacksParams{EntitlementAddonIds: line.ReleasedAddonIds, EntitlementID: current.ID})
	if err != nil {
		return false, err
	}
	return int(count) == len(line.ReleasedAddonIds), nil
}

// settleCapacityChange turns a paid capacity change into the state it bought:
// its credit reaches the wallet, its payment is recorded, the packs it
// replaces are taken off the live entitlement, the pack it adds is applied,
// and one `set_limits` operation pushes the result, all in the paying
// transaction.
func (store *Store) settleCapacityChange(ctx context.Context, tx pgx.Tx, queries *dbgen.Queries, order dbgen.Order, correlationID string) error {
	if err := store.postOrderCredit(ctx, queries, order); err != nil {
		return err
	}
	if err := store.recordOrderRevenue(ctx, queries, order, correlationID); err != nil {
		return err
	}
	entitlement, err := queries.GetLatestEntitlementForChange(ctx, dbgen.GetLatestEntitlementForChangeParams{UserID: order.UserID, SubscriptionID: order.SubscriptionID})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.recordAddonOrphan(ctx, queries, order)
	}
	if err != nil {
		return err
	}
	line, err := queries.GetOrderCredit(ctx, order.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	for _, packID := range line.ReleasedAddonIds {
		pack, releaseErr := queries.ReleaseEntitlementAddon(ctx, dbgen.ReleaseEntitlementAddonParams{EntitlementAddonID: packID, OrderID: order.ID})
		if errors.Is(releaseErr, pgx.ErrNoRows) {
			// Released already: a replayed settlement, or another change
			// took it first and its capacity is gone from the entitlement.
			continue
		}
		if releaseErr != nil {
			return releaseErr
		}
		if pack.EntitlementID != entitlement.ID {
			continue
		}
		if entitlement, err = store.takeBackPack(ctx, queries, entitlement, pack); err != nil {
			return err
		}
	}
	if entitlement, err = store.applyAddonLines(ctx, queries, order, entitlement); err != nil {
		return err
	}
	desired, err := json.Marshal(map[string]any{
		"effectiveAt": store.clock().UTC(), "endsAt": entitlement.EndsAt.Time,
		"trafficAllowanceBytes": nullableInt8(entitlement.TrafficAllowanceBytes),
		"deviceLimit":           nullableInt4(entitlement.DeviceLimit),
		"squadIds":              databaseutil.UUIDStrings(entitlement.RemnawaveSquadIds),
	})
	if err != nil {
		return err
	}
	operation, err := queries.CreateFulfillmentOperation(ctx, dbgen.CreateFulfillmentOperationParams{
		EntitlementID: entitlement.ID, Operation: "set_limits",
		IdempotencyKey: "order:" + uuidString(order.ID) + ":change", CorrelationID: correlationID,
		DesiredState: desired,
	})
	if err != nil {
		return err
	}
	if _, err = queries.InsertOutboxEvent(ctx, dbgen.InsertOutboxEventParams{Topic: "subscription.capacity_changed", Payload: []byte(fmt.Sprintf(`{"orderId":%q,"entitlementId":%q,"operation":%q}`, uuidString(order.ID), uuidString(entitlement.ID), order.Operation))}); err != nil {
		return err
	}
	if _, err = queries.MarkOrderFulfilled(ctx, order.ID); err != nil {
		return err
	}
	return store.enqueue(ctx, tx, uuidString(operation.ID))
}

// takeBackPack lowers the entitlement by exactly what one released pack had
// added, never below zero.
func (store *Store) takeBackPack(ctx context.Context, queries *dbgen.Queries, entitlement dbgen.Entitlement, pack dbgen.EntitlementAddon) (dbgen.Entitlement, error) {
	if pack.TrafficBytes.Valid && pack.TrafficBytes.Int64 > 0 {
		return queries.SubtractEntitlementTraffic(ctx, dbgen.SubtractEntitlementTrafficParams{EntitlementID: entitlement.ID, TrafficBytes: pack.TrafficBytes.Int64})
	}
	return entitlement, nil
}
