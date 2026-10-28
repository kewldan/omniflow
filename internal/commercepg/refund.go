package commercepg

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// What a refund does to what the order bought. Each value is carried on the
// operator's refund notice so nobody has to reconstruct it from the ledger.
const (
	// refundAccessRevoked: the period the order bought was taken off its
	// entitlement and Remnawave is being told.
	refundAccessRevoked = "revoked"
	// refundAccessRetired: the entitlement had not started or had never been
	// provisioned, and was withdrawn whole.
	refundAccessRetired = "retired"
	// refundAccessKept: a later change has already been built on the refunded
	// period, so the time cannot be taken back automatically.
	refundAccessKept = "kept"
	// refundWalletDebited: the refunded top-up was taken back out of the
	// wallet.
	refundWalletDebited = "debited"
	// refundWalletShort: the wallet no longer holds the refunded credit.
	refundWalletShort = "short"
)

// reverseRefundedOrder undoes, inside the refund's own transaction, what the
// refunded money bought. A refund that gave money back and left the access or
// the wallet credit in place paid the customer twice.
//
// It returns the facts for the operator notice. Only a full refund of a
// subscription order revokes access; a partial refund is a goodwill gesture
// whose consequence is the operator's call. A top-up refund always takes the
// refunded amount back out of the wallet, because that is the money returned.
func (store *Store) reverseRefundedOrder(ctx context.Context, tx pgx.Tx, queries *dbgen.Queries, order dbgen.Order, refundID string, amount commerce.Money) (map[string]any, error) {
	switch {
	case order.Operation == "topup":
		return store.debitRefundedTopUp(ctx, queries, order, refundID, amount)
	case commerce.SubscriptionOperation(order.Operation) && order.State == string(commerce.OrderRefunded):
		return store.revokeRefundedAccess(ctx, tx, queries, order, refundID)
	default:
		return nil, nil
	}
}

// revokeRefundedAccess takes the refunded period off the entitlement the order
// created and queues the change for Remnawave.
func (store *Store) revokeRefundedAccess(ctx context.Context, tx pgx.Tx, queries *dbgen.Queries, order dbgen.Order, refundID string) (map[string]any, error) {
	entitlement, err := queries.LockEntitlementByOrder(ctx, order.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	facts := map[string]any{"entitlementId": uuidString(entitlement.ID)}
	if entitlement.Status == "superseded" {
		// Retired already. One that was provisioned was retired by a later
		// renewal or change built on top of this period, which cannot be
		// unpicked automatically; one that never reached Remnawave delivered
		// nothing.
		facts["access"] = refundAccessRetired
		if entitlement.RemnawaveUserID.Valid {
			facts["access"] = refundAccessKept
		}
		return facts, nil
	}
	spec, err := queries.GetOrderEntitlementSpec(ctx, order.ID)
	if err != nil {
		return nil, err
	}
	now := store.clock().UTC()
	revocation := commerce.RevokeRefundedPeriod(now, entitlement.StartsAt.Time, entitlement.EndsAt.Time, entitlement.PausedAt.Time, time.Duration(spec.DurationSeconds)*time.Second)
	if revocation.Retire {
		// Nothing was delivered: an at_expiry downgrade still waiting for the
		// current period to end. It is withdrawn whole, and its snoozed
		// `create` is cancelled when it wakes, because its entitlement is
		// retired.
		if _, err = queries.RetireUnprovisionedEntitlement(ctx, entitlement.ID); err != nil {
			return nil, err
		}
		facts["access"] = refundAccessRetired
		return facts, nil
	}
	shortened, err := queries.ShortenEntitlement(ctx, dbgen.ShortenEntitlementParams{EntitlementID: entitlement.ID, EndsAt: pgtype.Timestamptz{Time: revocation.EndsAt, Valid: true}})
	if err != nil {
		return nil, err
	}
	facts["access"], facts["endsAt"] = refundAccessRevoked, shortened.EndsAt.Time
	if shortened.Status == "pending" {
		// Never provisioned. Its `create` is cancelled when it runs, because
		// the order is refunded; one already talking to Remnawave records
		// what it did, and the scheduled reconcile then pushes this shorter
		// end. Nothing needs to be queued here.
		return facts, nil
	}
	desired, err := json.Marshal(map[string]any{
		"endsAt":                shortened.EndsAt.Time,
		"trafficAllowanceBytes": nullableInt8(shortened.TrafficAllowanceBytes),
		"deviceLimit":           nullableInt4(shortened.DeviceLimit),
		"squadIds":              databaseutil.UUIDStrings(shortened.RemnawaveSquadIds),
	})
	if err != nil {
		return nil, err
	}
	operation, err := queries.CreateFulfillmentOperation(ctx, dbgen.CreateFulfillmentOperationParams{
		EntitlementID: shortened.ID, Operation: "reconcile",
		IdempotencyKey: "refund:" + refundID + ":revoke", CorrelationID: "refund:" + refundID,
		DesiredState: desired,
	})
	if err != nil {
		return nil, err
	}
	if store.enqueue != nil {
		if err = store.enqueue(ctx, tx, uuidString(operation.ID)); err != nil {
			return nil, err
		}
	}
	return facts, nil
}

// debitRefundedTopUp takes a refunded top-up back out of the wallet. The money
// went back to the customer through the provider, so the credit it bought
// must not stay spendable. When the wallet no longer holds it — the customer
// spent it, or an unpaid order has it reserved — nothing is debited and the
// operator is told: a negative balance is not a state the ledger allows.
func (store *Store) debitRefundedTopUp(ctx context.Context, queries *dbgen.Queries, order dbgen.Order, refundID string, amount commerce.Money) (map[string]any, error) {
	available, err := queries.GetAvailableWalletBalance(ctx, dbgen.GetAvailableWalletBalanceParams{TargetUserID: order.UserID, TargetCurrency: amount.Currency})
	if err != nil {
		return nil, err
	}
	if available < amount.Amount {
		store.notifyOperator(ctx, queries, "incident", "refund:"+refundID+":wallet", map[string]any{
			"orderId": uuidString(order.ID), "operation": order.Operation, "currency": amount.Currency,
			"amountMinor": amount.Amount, "status": "topup_refund_wallet_short",
		})
		return map[string]any{"wallet": refundWalletShort}, nil
	}
	entries := []commerce.LedgerEntry{
		{AccountType: "customer_wallet", CustomerID: uuidString(order.UserID), Currency: amount.Currency, AmountMinor: -amount.Amount},
		{AccountType: "platform_clearing", Currency: amount.Currency, AmountMinor: amount.Amount},
	}
	if err = commerce.ValidateLedger(entries); err != nil {
		return nil, err
	}
	transaction, err := queries.CreateLedgerTransaction(ctx, dbgen.CreateLedgerTransactionParams{
		Type: "debit", ReferenceType: "refund", ReferenceID: refundID,
		IdempotencyKey: "refund-debit:" + refundID,
		Reason:         pgtype.Text{String: "refunded wallet top-up", Valid: true},
	})
	if err != nil {
		return nil, err
	}
	if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "customer_wallet", UserID: order.UserID, Currency: amount.Currency, AmountMinor: -amount.Amount}); err != nil {
		return nil, err
	}
	if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "platform_clearing", Currency: amount.Currency, AmountMinor: amount.Amount}); err != nil {
		return nil, err
	}
	return map[string]any{"wallet": refundWalletDebited}, nil
}
