package commercepg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Grant modes an operator chooses on the customer card.
const (
	// GrantDays adds days of a plan, after the current period.
	GrantDays = "days"
	// GrantTrial gives the installation's trial to a customer without access.
	GrantTrial = "trial"
	// GrantChangePlan puts another plan in place of the current one for the
	// time the current one has left, at no charge.
	GrantChangePlan = "change_plan"
)

// GrantInput is an operator's free grant.
type GrantInput struct {
	CustomerID    string
	PlanVersionID string
	Mode          string
	Days          int
	// IdempotencyKey makes a repeated click the same grant.
	IdempotencyKey string
}

// ErrGrantRefused explains why a grant could not apply: a trial for somebody
// who has access or already had one, or a plan change with no access to change.
var ErrGrantRefused = errors.New("the grant does not apply to this customer")

// ErrInsufficientBalance refuses a purchase on the customer's behalf that the
// wallet does not cover.
var ErrInsufficientBalance = errors.New("the wallet does not cover this purchase")

// GrantPlan gives a customer days, a trial, or a plan change without a
// payment. It writes a paid "code" order, an entitlement, and the fulfillment
// job in one serializable transaction — the path a redeemed access code takes
// — so the grant is provisioned and audited like any other order.
func (store *Store) GrantPlan(ctx context.Context, input GrantInput) (time.Time, string, error) {
	userID, err := parseUUID(input.CustomerID)
	if err != nil {
		return time.Time{}, "", err
	}
	if len(input.IdempotencyKey) < 8 || len(input.IdempotencyKey) > 128 {
		return time.Time{}, "", fmt.Errorf("%w: idempotency key", ErrGrantRefused)
	}
	var endsAt time.Time
	var orderID pgtype.UUID
	err = retrySerializable(ctx, func() error {
		tx, beginErr := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
		if beginErr != nil {
			return beginErr
		}
		defer func() { _ = tx.Rollback(ctx) }()
		queries := dbgen.New(tx)
		key := "grant:" + input.IdempotencyKey
		if existing, lookupErr := queries.GetOrderByIdempotency(ctx, dbgen.GetOrderByIdempotencyParams{UserID: userID, IdempotencyKey: key}); lookupErr == nil {
			orderID = existing.ID
			return nil
		}
		var planVersionID pgtype.UUID
		var currency string
		if input.Mode == GrantTrial && input.PlanVersionID == "" {
			trial, trialErr := queries.GetTrialPlanVersionForReward(ctx)
			if errors.Is(trialErr, pgx.ErrNoRows) {
				return ErrGrantRefused
			}
			if trialErr != nil {
				return trialErr
			}
			planVersionID, currency = trial.ID, trial.Currency
		} else {
			if planVersionID, err = parseUUID(input.PlanVersionID); err != nil {
				return err
			}
			if currency, err = queries.GetPlanVersionCurrency(ctx, planVersionID); err != nil {
				return fmt.Errorf("%w: plan version", ErrGrantRefused)
			}
		}
		duration := time.Duration(input.Days) * 24 * time.Hour
		switch input.Mode {
		case GrantDays, GrantTrial:
			if input.Days <= 0 || input.Days > 3650 {
				return fmt.Errorf("%w: days", ErrGrantRefused)
			}
		case GrantChangePlan:
			duration = time.Hour // replaced by the time the current plan has left
		default:
			return fmt.Errorf("%w: mode", ErrGrantRefused)
		}
		var grantErr error
		endsAt, orderID, grantErr = store.grantDays(ctx, tx, queries, userID, planVersionID, currency,
			duration, input.Mode == GrantTrial, input.Mode == GrantChangePlan, key)
		if errors.Is(grantErr, ErrPromoNotApplicable) {
			return ErrGrantRefused
		}
		if grantErr != nil {
			return grantErr
		}
		return tx.Commit(ctx)
	})
	return endsAt, uuidString(orderID), err
}

// BuyFromBalance buys a plan version for the customer with their wallet, on
// the operator's request. It is refused before any order exists when the
// wallet does not cover the whole price, so an operator never leaves a
// half-paid order behind; otherwise it is an ordinary order the wallet paid.
func (store *Store) BuyFromBalance(ctx context.Context, input CreateOrderInput) (dbgen.Order, error) {
	input.SkipWallet = false
	quote, err := store.PreviewOrder(ctx, input)
	if err != nil {
		return dbgen.Order{}, err
	}
	if quote.ExternalMinor > 0 {
		return dbgen.Order{}, ErrInsufficientBalance
	}
	return store.CreateOrder(ctx, input)
}
