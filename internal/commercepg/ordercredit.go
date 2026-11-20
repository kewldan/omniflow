package commercepg

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Kinds of credit line.
const (
	// CreditPlanRemainder is the unused remainder of a plan period a change
	// throws away.
	CreditPlanRemainder = "plan_remainder"
)

// pendingCredit is a credit line priced for an order that is not open yet.
type pendingCredit struct {
	kind          string
	entitlementID pgtype.UUID
	sourceOrderID pgtype.UUID
	credit        commerce.RemainderCredit
}

// creditPayingOperations are the orders whose payment a remainder credit may
// hand back. A gift's order is the buyer's, a code and a trial paid nothing,
// and an operator's grant was never paid for: handing their remainder back as
// wallet credit would turn free time into money.
var creditPayingOperations = map[string]bool{
	"purchase": true, "extension": true, "renewal": true, "upgrade": true, "downgrade": true,
}

// planRemainder prices the remainder of the subscription's current period
// that a plan change would throw away, or nil when there is nothing to credit.
// subscriptionID may be empty, which is the customer's primary subscription —
// the one order creation resolves an unnamed change to.
func (store *Store) planRemainder(ctx context.Context, queries *dbgen.Queries, userID pgtype.UUID, subscriptionID, currency string) (*pendingCredit, error) {
	var target pgtype.UUID
	if subscriptionID != "" {
		parsed, err := parseUUID(subscriptionID)
		if err != nil {
			return nil, nil
		}
		target = parsed
	} else {
		primary, err := queries.GetPrimarySubscription(ctx, userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		target = primary.ID
	}
	current, err := queries.GetLatestEntitlementForChange(ctx, dbgen.GetLatestEntitlementForChangeParams{UserID: userID, SubscriptionID: target})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := store.clock().UTC()
	if !commerce.EntitlementLive(current.Status, current.EndsAt.Time, now) {
		return nil, nil
	}
	return store.remainderOf(ctx, queries, current, currency, now)
}

// remainderOf prices the remainder of one entitlement at what its own order
// paid, or nil when it has none to hand back.
func (store *Store) remainderOf(ctx context.Context, queries *dbgen.Queries, current dbgen.Entitlement, currency string, now time.Time) (*pendingCredit, error) {
	basis, err := queries.GetRemainderBasis(ctx, current.OrderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if basis.UserID != current.UserID || basis.Currency != currency || !creditPayingOperations[basis.Operation] {
		return nil, nil
	}
	posted, err := queries.PlanRemainderPosted(ctx, current.ID)
	if err != nil || posted {
		return nil, err
	}
	end := commerce.EffectiveEndsAt(now, current.EndsAt.Time, current.PausedAt.Time)
	credit := commerce.CreditRemainder(basis.PaidMinor, time.Duration(basis.PeriodSeconds)*time.Second, now, current.StartsAt.Time, end)
	if credit.CreditMinor <= 0 {
		return nil, nil
	}
	return &pendingCredit{kind: CreditPlanRemainder, entitlementID: current.ID, sourceOrderID: basis.OrderID, credit: credit}, nil
}

// recordOrderCredit writes the credit line an order was priced with.
func recordOrderCredit(ctx context.Context, queries *dbgen.Queries, order dbgen.Order, pending *pendingCredit) error {
	if pending == nil || pending.credit.CreditMinor <= 0 {
		return nil
	}
	_, err := queries.InsertOrderCredit(ctx, dbgen.InsertOrderCreditParams{
		OrderID: order.ID, UserID: order.UserID, Kind: pending.kind,
		SourceEntitlementID: pending.entitlementID, SourceOrderID: pending.sourceOrderID,
		Currency: order.Currency, BasisMinor: pending.credit.BasisMinor,
		RemainingSeconds: int64(pending.credit.Remaining / time.Second),
		PeriodSeconds:    max(int64(pending.credit.Period/time.Second), 1),
		CreditMinor:      pending.credit.CreditMinor,
	})
	return err
}

// creditablePending is what an order's credit line will post when it settles
// now: its amount when it is still owed, zero when it is not or there is none.
// The settlement's wallet re-check adds it to the balance, because the credit
// lands in the wallet in the same transaction that spends it.
func (store *Store) creditablePending(ctx context.Context, queries *dbgen.Queries, order dbgen.Order) (int64, error) {
	line, err := queries.GetOrderCredit(ctx, order.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil || line.CreditedAt.Valid {
		return 0, err
	}
	owed, err := store.creditStillOwed(ctx, queries, order, line)
	if err != nil || !owed {
		return 0, err
	}
	return line.CreditMinor, nil
}

// creditStillOwed re-checks a credit line at settlement. A plan remainder is
// owed while the entitlement it was priced on is still the subscription's
// current one and no other order has handed it back: a second change opened
// beside the first, or a renewal that settled in between, must not be paid
// for the same days twice.
func (store *Store) creditStillOwed(ctx context.Context, queries *dbgen.Queries, order dbgen.Order, line dbgen.OrderCredit) (bool, error) {
	if line.CreditMinor <= 0 || line.Currency != order.Currency {
		return false, nil
	}
	if line.Kind != CreditPlanRemainder {
		return false, nil
	}
	posted, err := queries.PlanRemainderPosted(ctx, line.SourceEntitlementID)
	if err != nil || posted {
		return false, err
	}
	current, err := queries.GetLatestEntitlementForChange(ctx, dbgen.GetLatestEntitlementForChangeParams{UserID: order.UserID, SubscriptionID: order.SubscriptionID})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return current.ID == line.SourceEntitlementID, nil
}

// postOrderCredit posts an order's credit line to the ledger: a `credit`
// transaction from platform clearing into the customer's wallet, keyed on the
// order so a replayed settlement posts it once. It runs before the order's own
// payment is recorded, so the wallet part of the order spends money that is
// already there. A line that is no longer owed is left unposted and recorded
// for an operator; the settlement's wallet re-check has already decided
// whether the order can settle without it.
func (store *Store) postOrderCredit(ctx context.Context, queries *dbgen.Queries, order dbgen.Order) error {
	line, err := queries.LockOrderCredit(ctx, order.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil || line.CreditedAt.Valid {
		return err
	}
	owed, err := store.creditStillOwed(ctx, queries, order, line)
	if err != nil {
		return err
	}
	if !owed {
		metadata, _ := json.Marshal(map[string]any{"orderId": uuidString(order.ID), "kind": line.Kind, "creditMinor": line.CreditMinor, "currency": line.Currency})
		_, err = queries.InsertAuditEvent(ctx, dbgen.InsertAuditEventParams{
			ActorType: "system", Action: "order.credit_withdrawn", TargetType: "order",
			Category: "financial", Outcome: "failure", TargetID: uuidString(order.ID),
			Reason:   pgtype.Text{String: "the credited remainder was no longer owed at settlement", Valid: true},
			Metadata: metadata,
		})
		return err
	}
	key := "order-credit:" + uuidString(order.ID)
	transaction, err := queries.CreateLedgerTransaction(ctx, dbgen.CreateLedgerTransactionParams{
		Type: "credit", ReferenceType: "order_credit", ReferenceID: uuidString(order.ID), IdempotencyKey: key,
		Reason: pgtype.Text{String: "unused remainder credited: " + line.Kind, Valid: true},
	})
	if err != nil {
		return err
	}
	entries := []commerce.LedgerEntry{
		{AccountType: "customer_wallet", CustomerID: uuidString(order.UserID), Currency: order.Currency, AmountMinor: line.CreditMinor},
		{AccountType: "platform_clearing", Currency: order.Currency, AmountMinor: -line.CreditMinor},
	}
	if err = commerce.ValidateLedger(entries); err != nil {
		return err
	}
	if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "customer_wallet", UserID: order.UserID, Currency: order.Currency, AmountMinor: line.CreditMinor}); err != nil {
		return err
	}
	if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "platform_clearing", Currency: order.Currency, AmountMinor: -line.CreditMinor}); err != nil {
		return err
	}
	_, err = queries.MarkOrderCreditPosted(ctx, dbgen.MarkOrderCreditPostedParams{OrderID: order.ID, LedgerTransactionID: transaction.ID})
	return err
}
