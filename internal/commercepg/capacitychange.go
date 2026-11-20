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
	OperationTrafficChange   = "traffic_change"
	OperationDeviceReduction = "device_reduction"
	OperationSquadRemoval    = "squad_removal"
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
	// releasedSlots is how many device slots a reduction gives back, taken
	// from the released packs in order; zero releases them whole.
	releasedSlots                                 int
	currentDevices, targetDevices, minimumDevices int
	excessDevices                                 int
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
	// CurrentDeviceLimit and TargetDeviceLimit are a device reduction's limit
	// before and after; MinimumDeviceLimit is the lowest it may go, what the
	// plan includes. ExcessDevices is how many connected devices the new limit
	// would leave over: the reduction is refused until they are gone.
	CurrentDeviceLimit int
	TargetDeviceLimit  int
	MinimumDeviceLimit int
	ExcessDevices      int
	ReleasedPacks      int
	EndsAt             time.Time
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
	// DeviceLimit is the limit a device reduction asks for, and
	// ConnectedDevices how many devices Remnawave reports connected now.
	DeviceLimit      int
	ConnectedDevices int
	// AddonPackID is the squad add-on pack a removal takes off.
	AddonPackID string
	SkipWallet  bool
	ExpiresAt   time.Time
	Provider    string
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
		CurrentDeviceLimit: change.currentDevices, TargetDeviceLimit: change.targetDevices,
		MinimumDeviceLimit: change.minimumDevices, ExcessDevices: change.excessDevices,
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
		ReleasedAddonIds: released, ReleasedSlots: int32(change.releasedSlots),
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
	if line.ReleasedSlots > 0 {
		slots, slotErr := queries.CountReleasableSlots(ctx, dbgen.CountReleasableSlotsParams{EntitlementAddonIds: line.ReleasedAddonIds, EntitlementID: current.ID})
		return slots >= line.ReleasedSlots, slotErr
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
	if line.ReleasedSlots > 0 {
		if entitlement, err = store.releaseSlots(ctx, queries, order, entitlement, line); err != nil {
			return err
		}
		line.ReleasedAddonIds = nil
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
	if len(pack.RemnawaveSquadIds) > 0 {
		return store.withdrawPackSquads(ctx, queries, entitlement, pack)
	}
	return entitlement, nil
}

// withdrawPackSquads takes off the entitlement the squads a released pack
// alone granted. A squad the plan, the customer's own selection, or another
// pack still grants stays.
func (store *Store) withdrawPackSquads(ctx context.Context, queries *dbgen.Queries, entitlement dbgen.Entitlement, pack dbgen.EntitlementAddon) (dbgen.Entitlement, error) {
	keep := map[pgtype.UUID]bool{}
	base, err := queries.GetEntitlementBaseSquads(ctx, entitlement.ID)
	if err != nil {
		return entitlement, err
	}
	for _, squad := range append(base.SelectedSquadIds, base.PlanSquadIds...) {
		keep[squad] = true
	}
	others, err := queries.ListLivePacks(ctx, entitlement.ID)
	if err != nil {
		return entitlement, err
	}
	for _, other := range others {
		for _, squad := range other.RemnawaveSquadIds {
			keep[squad] = true
		}
	}
	withdrawn := map[pgtype.UUID]bool{}
	for _, squad := range pack.RemnawaveSquadIds {
		if !keep[squad] {
			withdrawn[squad] = true
		}
	}
	if len(withdrawn) == 0 {
		return entitlement, nil
	}
	remaining := make([]pgtype.UUID, 0, len(entitlement.RemnawaveSquadIds))
	for _, squad := range entitlement.RemnawaveSquadIds {
		if !withdrawn[squad] {
			remaining = append(remaining, squad)
		}
	}
	return queries.SetEntitlementSquads(ctx, dbgen.SetEntitlementSquadsParams{EntitlementID: entitlement.ID, SquadIds: remaining})
}

// SquadRemovalOption is one paid squad add-on a subscription may remove,
// with what removing it hands back.
type SquadRemovalOption struct {
	PackID         string
	AddonVersionID string
	SquadIDs       []string
	Quote          CapacityChangeQuote
}

// planSquadRemoval prices taking one squad add-on pack off the subscription:
// nothing is charged and what is left of the pack is credited at what its
// order took for it.
func (store *Store) planSquadRemoval(ctx context.Context, queries *dbgen.Queries, userID pgtype.UUID, input CapacityChangeInput) (capacityChange, error) {
	entitlement, err := store.liveForChange(ctx, queries, userID, input.SubscriptionID)
	if err != nil {
		return capacityChange{}, err
	}
	packID, err := parseUUID(input.AddonPackID)
	if err != nil {
		return capacityChange{}, commerce.ErrSquadRemovalUnavailable
	}
	packs, err := queries.ListLivePacks(ctx, entitlement.ID)
	if err != nil {
		return capacityChange{}, err
	}
	for _, pack := range packs {
		if pack.ID != packID || pack.AddonKind != "squads" || len(pack.RemnawaveSquadIds) == 0 {
			continue
		}
		change := capacityChange{operation: OperationSquadRemoval, entitlement: entitlement}
		change.release(pack, packCredit(pack, entitlement, input.Currency, store.clock().UTC()))
		return change, nil
	}
	return capacityChange{}, commerce.ErrSquadRemovalUnavailable
}

// SquadRemovals lists the paid squad add-ons a live subscription may remove,
// each with what removing it now hands back.
func (store *Store) SquadRemovals(ctx context.Context, customerID, subscriptionID, currency string) ([]SquadRemovalOption, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return nil, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	entitlement, err := store.liveForChange(ctx, queries, userID, subscriptionID)
	if errors.Is(err, ErrNoActiveSubscription) || errors.Is(err, ErrSubscriptionUnknown) {
		return []SquadRemovalOption{}, nil
	}
	if err != nil {
		return nil, err
	}
	packs, err := queries.ListLivePacks(ctx, entitlement.ID)
	if err != nil {
		return nil, err
	}
	wallet, err := availableWallet(ctx, queries, userID, currency)
	if err != nil {
		return nil, err
	}
	options := []SquadRemovalOption{}
	for _, pack := range packs {
		if pack.AddonKind != "squads" || len(pack.RemnawaveSquadIds) == 0 {
			continue
		}
		change := capacityChange{operation: OperationSquadRemoval, entitlement: entitlement}
		change.release(pack, packCredit(pack, entitlement, currency, store.clock().UTC()))
		quote, _, quoteErr := quoteChange(change, currency, wallet, false)
		if quoteErr != nil {
			return nil, quoteErr
		}
		options = append(options, SquadRemovalOption{
			PackID: uuidString(pack.ID), AddonVersionID: uuidString(pack.AddonVersionID),
			SquadIDs: databaseutil.UUIDStrings(pack.RemnawaveSquadIds), Quote: quote,
		})
	}
	return options, nil
}

// CreateSquadRemovalOrder opens the order that removes one paid squad add-on.
// It charges nothing; what is left of the pack goes to the wallet, and the
// squads it alone granted leave the entitlement through one `set_limits`.
func (store *Store) CreateSquadRemovalOrder(ctx context.Context, input CapacityChangeInput) (dbgen.Order, error) {
	return store.createCapacityChangeOrder(ctx, input, OperationSquadRemoval, store.planSquadRemoval)
}

// releaseSlots gives back a device reduction's slots, from the packs its
// credit line names in order, and lowers the entitlement's device limit by
// exactly what was given back.
func (store *Store) releaseSlots(ctx context.Context, queries *dbgen.Queries, order dbgen.Order, entitlement dbgen.Entitlement, line dbgen.OrderCredit) (dbgen.Entitlement, error) {
	remaining := int(line.ReleasedSlots)
	given := 0
	for _, packID := range line.ReleasedAddonIds {
		if remaining <= 0 {
			break
		}
		released, err := queries.ReleaseEntitlementAddonSlots(ctx, dbgen.ReleaseEntitlementAddonSlotsParams{EntitlementAddonID: packID, Slots: int32(remaining), OrderID: order.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return entitlement, err
		}
		if released.EntitlementID != entitlement.ID {
			continue
		}
		remaining -= int(released.Taken)
		given += int(released.Taken)
	}
	if given == 0 {
		return entitlement, nil
	}
	return queries.SubtractEntitlementDevices(ctx, dbgen.SubtractEntitlementDevicesParams{EntitlementID: entitlement.ID, DeviceSlots: int32(given)})
}

// planDeviceReduction prices lowering a subscription's device limit by giving
// back slots bought as add-ons, newest first, each credited at what its order
// took for it. The limit cannot go below what the plan itself includes. A
// reduction is priced even while too many devices are connected, so the
// preview can say how many must go first.
func (store *Store) planDeviceReduction(ctx context.Context, queries *dbgen.Queries, userID pgtype.UUID, input CapacityChangeInput) (capacityChange, error) {
	entitlement, err := store.liveForChange(ctx, queries, userID, input.SubscriptionID)
	if err != nil {
		return capacityChange{}, err
	}
	if !entitlement.DeviceLimit.Valid {
		return capacityChange{}, commerce.ErrDeviceReductionUnavailable
	}
	packs, err := queries.ListLivePacks(ctx, entitlement.ID)
	if err != nil {
		return capacityChange{}, err
	}
	devicePacks := make([]dbgen.ListLivePacksRow, 0, len(packs))
	releasable := 0
	for _, pack := range packs {
		if pack.AddonKind != "devices" || !pack.DeviceSlots.Valid || pack.DeviceSlots.Int32 <= pack.ReleasedSlots {
			continue
		}
		devicePacks = append(devicePacks, pack)
		releasable += int(pack.DeviceSlots.Int32 - pack.ReleasedSlots)
	}
	if releasable == 0 {
		return capacityChange{}, commerce.ErrDeviceReductionUnavailable
	}
	current := int(entitlement.DeviceLimit.Int32)
	change := capacityChange{
		operation: OperationDeviceReduction, entitlement: entitlement,
		currentDevices: current, minimumDevices: max(current-releasable, 0), targetDevices: input.DeviceLimit,
	}
	if input.DeviceLimit == current {
		return change, commerce.ErrCapacityChangeSame
	}
	if input.DeviceLimit < change.minimumDevices || input.DeviceLimit > current {
		return change, commerce.ErrDeviceReductionUnavailable
	}
	change.excessDevices = max(input.ConnectedDevices-input.DeviceLimit, 0)
	now := store.clock().UTC()
	needed := current - input.DeviceLimit
	change.releasedSlots = needed
	for _, pack := range devicePacks {
		if needed <= 0 {
			break
		}
		take := min(needed, int(pack.DeviceSlots.Int32-pack.ReleasedSlots))
		whole := packCredit(pack, entitlement, input.Currency, now)
		change.release(pack, commerce.RemainderCredit{
			CreditMinor: commerce.ShareOf(whole.CreditMinor, take, int(pack.DeviceSlots.Int32)),
			BasisMinor:  commerce.ShareOf(whole.BasisMinor, take, int(pack.DeviceSlots.Int32)),
			Remaining:   whole.Remaining, Period: whole.Period,
		})
		needed -= take
	}
	return change, nil
}

// DeviceReductionQuote prices lowering a subscription's device limit to
// input.DeviceLimit without opening an order. ExcessDevices says how many of
// the connected devices must be disconnected first.
func (store *Store) DeviceReductionQuote(ctx context.Context, input CapacityChangeInput) (CapacityChangeQuote, error) {
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
	change, err := store.planDeviceReduction(ctx, queries, userID, input)
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

// DeviceLimitRange reports a subscription's device limit now and the lowest a
// reduction may take it to; ok is false when it has no bought slots to give
// back.
func (store *Store) DeviceLimitRange(ctx context.Context, customerID, subscriptionID string) (current, minimum int, ok bool, err error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return 0, 0, false, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return 0, 0, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	change, err := store.planDeviceReduction(ctx, dbgen.New(tx), userID, CapacityChangeInput{SubscriptionID: subscriptionID, DeviceLimit: -1})
	switch {
	case change.currentDevices > 0 && change.minimumDevices < change.currentDevices:
		return change.currentDevices, change.minimumDevices, true, nil
	case err == nil, errors.Is(err, commerce.ErrDeviceReductionUnavailable), errors.Is(err, commerce.ErrCapacityChangeSame),
		errors.Is(err, ErrNoActiveSubscription), errors.Is(err, ErrSubscriptionUnknown):
		return 0, 0, false, nil
	default:
		return 0, 0, false, err
	}
}

// CreateDeviceReductionOrder opens the order that lowers a subscription's
// device limit. It charges nothing; what is left of the slots it gives back
// is credited to the wallet. It is refused while more devices are connected
// than the new limit allows.
func (store *Store) CreateDeviceReductionOrder(ctx context.Context, input CapacityChangeInput) (dbgen.Order, error) {
	return store.createCapacityChangeOrder(ctx, input, OperationDeviceReduction,
		func(ctx context.Context, queries *dbgen.Queries, userID pgtype.UUID, input CapacityChangeInput) (capacityChange, error) {
			change, err := store.planDeviceReduction(ctx, queries, userID, input)
			if err == nil && change.excessDevices > 0 {
				return capacityChange{}, fmt.Errorf("%w: %d", commerce.ErrDevicesOverLimit, change.excessDevices)
			}
			return change, err
		})
}
