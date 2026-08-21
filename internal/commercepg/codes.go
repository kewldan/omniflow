package commercepg

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/accesscode"
	"github.com/omniflow/omniflow/internal/commerce"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Redeeming a wholesale code.
//
// A code from a batch and a gift code are the same sixteen characters, so the
// customer surfaces offer one box and try both. That is not a shortcut: a
// customer holding a code has no way to know which kind it is, and asking them
// to pick the right form would be asking them to know something only the
// operator does.

// ErrCodeNotRedeemable is the single refusal for every reason a code does not
// work: unknown, already used, revoked, or from an expired batch.
//
// One error rather than four, deliberately. Distinguishing them would turn the
// endpoint into an oracle that says which codes exist, which is exactly what
// somebody working through guesses wants to know.
var ErrCodeNotRedeemable = errors.New("code cannot be redeemed")

// RedeemedCode is what a redemption produced.
type RedeemedCode struct {
	BatchReference string
	EntitlementID  string
	SubscriptionID string
	EndsAt         time.Time
}

// RedeemAccessCode turns a wholesale code into an entitlement.
//
// Single redemption is a property of the UPDATE predicate: only an `issued`
// code in a batch that is neither revoked nor expired matches, and the same
// statement writes the redemption. Two simultaneous attempts on one code
// therefore produce one entitlement and one refusal without a lock anybody has
// to remember to take.
func (store *Store) RedeemAccessCode(
	ctx context.Context, code, customerID string,
) (RedeemedCode, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return RedeemedCode{}, err
	}
	normalized, err := accesscode.Normalize(code)
	if err != nil {
		return RedeemedCode{}, ErrCodeNotRedeemable
	}

	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RedeemedCode{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)

	redeemed, err := queries.RedeemAccessCode(ctx, dbgen.RedeemAccessCodeParams{
		CodeHash: accesscode.Hash(normalized), RedeemedBy: userID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return RedeemedCode{}, ErrCodeNotRedeemable
	}
	if err != nil {
		return RedeemedCode{}, err
	}

	batch, err := queries.GetCodeBatchForRedemption(ctx, redeemed.BatchID)
	if err != nil {
		return RedeemedCode{}, err
	}
	plan, err := queries.GetPlanVersionForOrder(ctx, dbgen.GetPlanVersionForOrderParams{
		PlanVersionID: batch.PlanVersionID, Currency: batch.Currency,
	})
	if err != nil {
		return RedeemedCode{}, err
	}

	subscriptionID, err := store.resolveSubscription(
		ctx, queries, userID, plan, CreateOrderInput{Operation: "purchase"},
	)
	if err != nil {
		return RedeemedCode{}, err
	}

	now := store.clock().UTC()
	currentEndsAt, err := store.changeBase(ctx, queries, userID, subscriptionID, now)
	if err != nil {
		return RedeemedCode{}, err
	}
	// A code extends what the customer already has rather than replacing it, for
	// the same reason a gift does: replacing would mean a code that shortens
	// somebody's subscription.
	operation := "purchase"
	if currentEndsAt != nil && currentEndsAt.After(now) {
		operation = "extension"
	}
	schedule, err := commerce.ScheduleEntitlement(
		now, time.Duration(plan.DurationSeconds)*time.Second, operation,
		plan.UpgradePolicy, plan.DowngradePolicy, currentEndsAt,
	)
	if err != nil {
		return RedeemedCode{}, err
	}

	// A zero-value order, so the entitlement has the transaction every
	// entitlement in this installation has. No money arrived here: the
	// distributor paid outside the product, and the price they agreed is on the
	// batch. An order carrying the wholesale price would put revenue in the
	// sales report on a day nobody paid anything.
	order, err := queries.CreateOrder(ctx, dbgen.CreateOrderParams{
		UserID: userID, State: "paid", Operation: "code",
		Currency: batch.Currency, SubtotalMinor: 0, DiscountMinor: 0,
		WalletMinor: 0, ExternalMinor: 0,
		IdempotencyKey: "code:" + uuidString(redeemed.ID),
		SubscriptionID: subscriptionID,
		// A batch grants exactly what its plan version says, so there is no
		// selection to carry: the codes were printed before any customer
		// existed, and offering one a choice at redemption would mean two
		// holders of the same batch getting different things.
		SelectedSquadIds: noSquads(),
	})
	if err != nil {
		return RedeemedCode{}, err
	}

	entitlement, err := queries.CreateEntitlement(ctx, dbgen.CreateEntitlementParams{
		UserID: userID, OrderID: order.ID, PlanVersionID: plan.ID,
		StartsAt:              pgtype.Timestamptz{Time: schedule.StartsAt, Valid: true},
		EndsAt:                pgtype.Timestamptz{Time: schedule.EndsAt, Valid: true},
		TrafficAllowanceBytes: plan.TrafficAllowanceBytes,
		DeviceLimit:           plan.DeviceLimit,
		RemnawaveSquadIds:     plan.RemnawaveSquadIds,
		SubscriptionID:        subscriptionID,
	})
	if err != nil {
		return RedeemedCode{}, err
	}
	if err := queries.AttachAccessCodeEntitlement(ctx, dbgen.AttachAccessCodeEntitlementParams{
		ID: redeemed.ID, RedeemedEntitlementID: entitlement.ID,
	}); err != nil {
		return RedeemedCode{}, err
	}

	desired, _ := json.Marshal(map[string]any{
		"effectiveAt": schedule.EffectiveAt, "endsAt": schedule.EndsAt,
		"trafficAllowanceBytes": nullableInt8(entitlement.TrafficAllowanceBytes),
		"deviceLimit":           nullableInt4(entitlement.DeviceLimit),
		"squadIds":              databaseutil.UUIDStrings(entitlement.RemnawaveSquadIds),
		"resetTraffic":          commerce.ResetsTraffic(operation),
	})
	operationRow, err := queries.CreateFulfillmentOperation(ctx, dbgen.CreateFulfillmentOperationParams{
		EntitlementID: entitlement.ID, Operation: "create",
		IdempotencyKey: "code:" + uuidString(redeemed.ID) + ":fulfill",
		CorrelationID:  "code:" + uuidString(redeemed.ID),
		DesiredState:   desired,
	})
	if err != nil {
		return RedeemedCode{}, err
	}
	// The job is inserted in this transaction, so provisioning is queued if and
	// only if the redemption commits. A code marked used with nothing
	// provisioned would be the worst outcome available here: the customer has
	// spent a code and has nothing.
	if store.enqueue != nil {
		if err = store.enqueue(ctx, tx, uuidString(operationRow.ID)); err != nil {
			return RedeemedCode{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return RedeemedCode{}, err
	}
	return RedeemedCode{
		EntitlementID:  uuidString(entitlement.ID),
		SubscriptionID: uuidString(subscriptionID),
		EndsAt:         schedule.EndsAt,
	}, nil
}
