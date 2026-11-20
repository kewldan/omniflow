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
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// TrafficResetQuote is what restarting one subscription's traffic counter
// costs now, or why it cannot be bought.
type TrafficResetQuote struct {
	Available   bool
	AmountMinor int64
	Currency    string
}

// TrafficResetOrderInput is a customer's paid traffic reset.
type TrafficResetOrderInput struct {
	CustomerID     string
	SubscriptionID string
	Currency       string
	IdempotencyKey string
}

// liveForReset resolves the subscription's live entitlement and the price of
// a reset on its plan version.
func (store *Store) liveForReset(ctx context.Context, queries *dbgen.Queries, userID pgtype.UUID, subscriptionID, currency string) (dbgen.Entitlement, int64, error) {
	target, err := parseUUID(subscriptionID)
	if err != nil {
		return dbgen.Entitlement{}, 0, ErrSubscriptionUnknown
	}
	if _, err = queries.GetCustomerSubscription(ctx, dbgen.GetCustomerSubscriptionParams{SubscriptionID: target, UserID: userID}); err != nil {
		return dbgen.Entitlement{}, 0, ErrSubscriptionUnknown
	}
	entitlement, err := queries.GetLatestEntitlementForChange(ctx, dbgen.GetLatestEntitlementForChangeParams{UserID: userID, SubscriptionID: target})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.Entitlement{}, 0, ErrNoActiveSubscription
	}
	if err != nil {
		return dbgen.Entitlement{}, 0, err
	}
	if entitlement.Status == "paused" || !commerce.EntitlementLive(entitlement.Status, entitlement.EndsAt.Time, store.clock()) {
		return dbgen.Entitlement{}, 0, ErrNoActiveSubscription
	}
	if !entitlement.TrafficAllowanceBytes.Valid {
		return dbgen.Entitlement{}, 0, commerce.ErrTrafficResetUnavailable
	}
	plan, err := queries.GetPlanVersionForOrder(ctx, dbgen.GetPlanVersionForOrderParams{PlanVersionID: entitlement.PlanVersionID, Currency: currency})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.Entitlement{}, 0, commerce.ErrTrafficResetUnavailable
	}
	if err != nil {
		return dbgen.Entitlement{}, 0, err
	}
	if !plan.TrafficResetPriceBps.Valid {
		return dbgen.Entitlement{}, 0, commerce.ErrTrafficResetUnavailable
	}
	price, err := commerce.TrafficResetPrice(plan.AmountMinor, int(plan.TrafficResetPriceBps.Int32))
	return entitlement, price, err
}

// TrafficResetQuote prices a reset for a subscription without opening an
// order. A subscription that cannot buy one answers Available false.
func (store *Store) TrafficResetQuote(ctx context.Context, customerID, subscriptionID, currency string) (TrafficResetQuote, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return TrafficResetQuote{}, err
	}
	_, price, err := store.liveForReset(ctx, dbgen.New(store.pool), userID, subscriptionID, currency)
	switch {
	case errors.Is(err, commerce.ErrTrafficResetUnavailable), errors.Is(err, ErrNoActiveSubscription):
		return TrafficResetQuote{Currency: currency}, nil
	case err != nil:
		return TrafficResetQuote{}, err
	}
	return TrafficResetQuote{Available: true, AmountMinor: price, Currency: currency}, nil
}

// CreateTrafficResetOrder opens the order for a paid traffic reset. It is an
// ordinary order: the wallet settles it when it covers the price, otherwise a
// provider does, and settlement queues the `reset_traffic` operation keyed on
// the order, so a replayed settlement resets once.
func (store *Store) CreateTrafficResetOrder(ctx context.Context, input TrafficResetOrderInput) (dbgen.Order, error) {
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
		if existing.Operation != "traffic_reset" || existing.Currency != input.Currency {
			return dbgen.Order{}, errors.New("idempotency key was already used with different parameters")
		}
		return existing, tx.Commit(ctx)
	} else if !errors.Is(existingErr, pgx.ErrNoRows) {
		return dbgen.Order{}, existingErr
	}
	if err = store.assertOperational(ctx, queries); err != nil {
		return dbgen.Order{}, err
	}
	entitlement, price, err := store.liveForReset(ctx, queries, userID, input.SubscriptionID, input.Currency)
	if err != nil {
		return dbgen.Order{}, err
	}
	wallet, err := availableWallet(ctx, queries, userID, input.Currency)
	if err != nil {
		return dbgen.Order{}, err
	}
	domainOrder, err := commerce.NewOrder("", input.CustomerID, commerce.Money{Amount: price, Currency: input.Currency}, 0, wallet)
	if err != nil {
		return dbgen.Order{}, err
	}
	state := string(commerce.OrderPending)
	if domainOrder.ExternalMinor == 0 {
		state = string(commerce.OrderPaid)
	}
	order, err := queries.CreateOrder(ctx, dbgen.CreateOrderParams{
		UserID: userID, State: state, Operation: "traffic_reset", Currency: input.Currency,
		SubtotalMinor: price, WalletMinor: domainOrder.WalletMinor, ExternalMinor: domainOrder.ExternalMinor,
		IdempotencyKey: input.IdempotencyKey, SubscriptionID: entitlement.SubscriptionID,
		ExpiresAt:        pgtype.Timestamptz{Time: store.paymentDeadline(time.Time{}, ""), Valid: true},
		SelectedSquadIds: noSquads(),
	})
	if err != nil {
		return dbgen.Order{}, err
	}
	if state == string(commerce.OrderPaid) {
		if err = store.settlePaidOrder(ctx, tx, queries, order, "wallet:"+input.IdempotencyKey); err != nil {
			return dbgen.Order{}, err
		}
	}
	return order, tx.Commit(ctx)
}

// settleTrafficReset queues the reset a paid order bought, against the
// subscription's live entitlement, and marks the order fulfilled. With no live
// entitlement left the money stays recorded and an operator is told, as with
// an add-on that had nothing to attach to.
func (store *Store) settleTrafficReset(ctx context.Context, tx pgx.Tx, queries *dbgen.Queries, order dbgen.Order, correlationID string) error {
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
	desired, _ := json.Marshal(map[string]any{"effectiveAt": store.clock().UTC(), "endsAt": entitlement.EndsAt.Time})
	operation, err := queries.CreateFulfillmentOperation(ctx, dbgen.CreateFulfillmentOperationParams{
		EntitlementID: entitlement.ID, Operation: "reset_traffic",
		IdempotencyKey: "order:" + uuidString(order.ID) + ":reset", CorrelationID: correlationID,
		DesiredState: desired,
	})
	if err != nil {
		return err
	}
	if _, err = queries.MarkOrderFulfilled(ctx, order.ID); err != nil {
		return err
	}
	return store.enqueue(ctx, tx, uuidString(operation.ID))
}
