package commercepg

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Reward promo kinds: codes redeemed on their own rather than at checkout.
const (
	RewardWalletCredit = "wallet_credit"
	RewardDays         = "days"
	RewardTrial        = "trial"
)

var (
	// ErrPromoNotReward reports a discount code: it applies at checkout only.
	ErrPromoNotReward = errors.New("this code is a discount for checkout")
	// ErrPromoNotApplicable reports a reward the customer cannot take: an
	// expired, exhausted, or ineligible code, a trial already used, or days
	// for a customer who never had a subscription.
	ErrPromoNotApplicable = errors.New("this code cannot be applied")
)

// RedeemedReward is what a reward code gave.
type RedeemedReward struct {
	Kind        string
	Days        int
	EndsAt      time.Time
	CreditMinor int64
	Currency    string
}

// RedeemRewardPromo applies a promo code that rewards on its own — days added
// to the subscription, a trial, or wallet credit — the way Bedolaga's promo
// codes work. Discount codes are refused with ErrPromoNotReward so the caller
// can point the customer at checkout.
//
// Every limit the checkout enforces applies here too: dates, eligibility, the
// promotion's and the code's total limits, and the per-customer limit. The
// redemption, its zero-amount order, and the reward commit together, under
// serializable isolation, so two redemptions racing for the last use cannot
// both succeed.
func (store *Store) RedeemRewardPromo(ctx context.Context, code, customerID string) (RedeemedReward, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return RedeemedReward{}, err
	}
	normalized, err := commerce.NormalizePromoCode(code)
	if err != nil {
		return RedeemedReward{}, ErrPromoNotApplicable
	}
	var reward RedeemedReward
	err = retrySerializable(ctx, func() error {
		var attemptErr error
		reward, attemptErr = store.redeemRewardPromo(ctx, normalized, userID)
		return attemptErr
	})
	return reward, err
}

func (store *Store) redeemRewardPromo(ctx context.Context, normalized string, userID pgtype.UUID) (RedeemedReward, error) {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return RedeemedReward{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)

	promo, err := queries.GetPromoForRedemption(ctx, normalized)
	if errors.Is(err, pgx.ErrNoRows) {
		return RedeemedReward{}, ErrPromoNotApplicable
	}
	if err != nil {
		return RedeemedReward{}, err
	}
	switch promo.Kind {
	case RewardWalletCredit, RewardDays, RewardTrial:
	default:
		return RedeemedReward{}, ErrPromoNotReward
	}
	now := store.clock().UTC()
	if promo.StartsAt.Valid && now.Before(promo.StartsAt.Time) || promo.EndsAt.Valid && !now.Before(promo.EndsAt.Time) {
		return RedeemedReward{}, ErrPromoNotApplicable
	}
	eligible, err := queries.CheckPromotionCustomerEligibility(ctx, dbgen.CheckPromotionCustomerEligibilityParams{
		Eligibility: promo.Eligibility, UserID: userID,
	})
	if err != nil || !eligible.Bool {
		return RedeemedReward{}, ErrPromoNotApplicable
	}
	counts, err := queries.CountPromoRedemptions(ctx, dbgen.CountPromoRedemptionsParams{
		PromotionID: promo.PromotionID, UserID: userID, PromoCodeID: promo.ID,
	})
	if err != nil {
		return RedeemedReward{}, err
	}
	if promo.PromotionRedemptionLimit.Valid && counts.TotalCount >= promo.PromotionRedemptionLimit.Int32 ||
		promo.RedemptionLimit.Valid && counts.CodeCount >= promo.RedemptionLimit.Int32 ||
		counts.CustomerCount >= promo.PerCustomerLimit {
		return RedeemedReward{}, ErrPromoNotApplicable
	}
	// One key per customer per use, so a retried request is the same redemption.
	key := "promo:" + uuidString(promo.ID) + ":" + uuidString(userID) + ":" + strconv.Itoa(int(counts.CustomerCount)+1)

	reward := RedeemedReward{Kind: promo.Kind}
	switch promo.Kind {
	case RewardWalletCredit:
		reward.CreditMinor, reward.Currency = promo.Value, promo.Currency.String
		order, orderErr := store.rewardOrder(ctx, queries, userID, promo.Currency.String, key, pgtype.UUID{})
		if orderErr != nil {
			return RedeemedReward{}, orderErr
		}
		if err = store.creditRewardWallet(ctx, queries, userID, promo.Currency.String, promo.Value, key); err != nil {
			return RedeemedReward{}, err
		}
		if _, err = queries.InsertPromoRedemption(ctx, dbgen.InsertPromoRedemptionParams{
			PromoCodeID: promo.ID, PromotionID: promo.PromotionID, UserID: userID, OrderID: order.ID,
		}); err != nil {
			return RedeemedReward{}, err
		}
	case RewardDays, RewardTrial:
		reward.Days = int(promo.Value)
		planVersionID, currency, planErr := store.rewardPlan(ctx, queries, userID, promo.Kind)
		if planErr != nil {
			return RedeemedReward{}, planErr
		}
		endsAt, orderID, grantErr := store.grantRewardDays(ctx, tx, queries, userID, planVersionID, currency,
			time.Duration(promo.Value)*24*time.Hour, promo.Kind == RewardTrial, key)
		if grantErr != nil {
			return RedeemedReward{}, grantErr
		}
		reward.EndsAt = endsAt
		if _, err = queries.InsertPromoRedemption(ctx, dbgen.InsertPromoRedemptionParams{
			PromoCodeID: promo.ID, PromotionID: promo.PromotionID, UserID: userID, OrderID: orderID,
		}); err != nil {
			return RedeemedReward{}, err
		}
	}
	return reward, tx.Commit(ctx)
}

// rewardPlan picks the plan version a days or trial reward is granted on:
// the customer's latest subscription for days, the live trial plan for a trial.
func (store *Store) rewardPlan(ctx context.Context, queries *dbgen.Queries, userID pgtype.UUID, kind string) (pgtype.UUID, string, error) {
	if kind == RewardTrial {
		trial, err := queries.GetTrialPlanVersionForReward(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, "", ErrPromoNotApplicable
		}
		return trial.ID, trial.Currency, err
	}
	latest, err := queries.GetLatestEntitlementAnyStatus(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Days extend a subscription; a customer who never had one has
		// nothing to extend.
		return pgtype.UUID{}, "", ErrPromoNotApplicable
	}
	if err != nil {
		return pgtype.UUID{}, "", err
	}
	currency, err := queries.GetPlanVersionCurrency(ctx, latest.PlanVersionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, "", ErrPromoNotApplicable
	}
	return latest.PlanVersionID, currency, err
}

func (store *Store) rewardOrder(
	ctx context.Context, queries *dbgen.Queries, userID pgtype.UUID, currency, key string, subscriptionID pgtype.UUID,
) (dbgen.Order, error) {
	return queries.CreateOrder(ctx, dbgen.CreateOrderParams{
		UserID: userID, State: "paid", Operation: "code", Currency: currency,
		IdempotencyKey: key, SubscriptionID: subscriptionID, SelectedSquadIds: noSquads(),
	})
}

// grantRewardDays adds duration to the customer's subscription on the given
// plan version, exactly as a redeemed access code does: an entitlement that
// starts where the current one ends, and a fulfillment job in this transaction.
func (store *Store) grantRewardDays(
	ctx context.Context, tx pgx.Tx, queries *dbgen.Queries, userID, planVersionID pgtype.UUID,
	currency string, duration time.Duration, trial bool, key string,
) (time.Time, pgtype.UUID, error) {
	plan, err := queries.GetPlanVersionForOrder(ctx, dbgen.GetPlanVersionForOrderParams{
		PlanVersionID: planVersionID, Currency: currency,
	})
	if err != nil {
		return time.Time{}, pgtype.UUID{}, err
	}
	subscriptionID, err := store.resolveSubscription(ctx, queries, userID, plan, CreateOrderInput{Operation: "purchase"})
	if err != nil {
		return time.Time{}, pgtype.UUID{}, err
	}
	now := store.clock().UTC()
	currentEndsAt, err := store.changeBase(ctx, queries, userID, subscriptionID, now)
	if err != nil {
		return time.Time{}, pgtype.UUID{}, err
	}
	operation := "purchase"
	if currentEndsAt != nil && currentEndsAt.After(now) {
		if trial {
			// A trial is for somebody without access.
			return time.Time{}, pgtype.UUID{}, ErrPromoNotApplicable
		}
		operation = "extension"
	}
	schedule, err := commerce.ScheduleEntitlement(now, duration, operation, plan.UpgradePolicy, plan.DowngradePolicy, currentEndsAt)
	if err != nil {
		return time.Time{}, pgtype.UUID{}, err
	}
	order, err := store.rewardOrder(ctx, queries, userID, currency, key, subscriptionID)
	if err != nil {
		return time.Time{}, pgtype.UUID{}, err
	}
	if trial {
		claimed, claimErr := queries.ClaimTrial(ctx, dbgen.ClaimTrialParams{UserID: userID, PlanID: plan.PlanID, OrderID: order.ID})
		if claimErr != nil {
			return time.Time{}, pgtype.UUID{}, claimErr
		}
		if claimed == 0 {
			return time.Time{}, pgtype.UUID{}, ErrPromoNotApplicable
		}
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
		return time.Time{}, pgtype.UUID{}, err
	}
	desired, _ := json.Marshal(map[string]any{
		"effectiveAt": schedule.EffectiveAt, "endsAt": schedule.EndsAt,
		"trafficAllowanceBytes": nullableInt8(entitlement.TrafficAllowanceBytes),
		"deviceLimit":           nullableInt4(entitlement.DeviceLimit),
		"squadIds":              databaseutil.UUIDStrings(entitlement.RemnawaveSquadIds),
		"resetTraffic":          store.resetsTraffic(ctx, queries, operation),
	})
	operationRow, err := queries.CreateFulfillmentOperation(ctx, dbgen.CreateFulfillmentOperationParams{
		EntitlementID: entitlement.ID, Operation: "create",
		IdempotencyKey: key + ":fulfill", CorrelationID: key, DesiredState: desired,
	})
	if err != nil {
		return time.Time{}, pgtype.UUID{}, err
	}
	if err = store.enqueue(ctx, tx, uuidString(operationRow.ID)); err != nil {
		return time.Time{}, pgtype.UUID{}, err
	}
	return schedule.EndsAt, order.ID, nil
}

// creditRewardWallet posts a balanced credit to the customer's wallet.
func (store *Store) creditRewardWallet(ctx context.Context, queries *dbgen.Queries, userID pgtype.UUID, currency string, amount int64, key string) error {
	transaction, err := queries.CreateLedgerTransaction(ctx, dbgen.CreateLedgerTransactionParams{
		Type: "credit", ReferenceType: "promotion", ReferenceID: key, IdempotencyKey: key,
		Reason: pgtype.Text{String: "Promo code reward", Valid: true},
	})
	if err != nil {
		return err
	}
	if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "customer_wallet", UserID: userID, Currency: currency, AmountMinor: amount}); err != nil {
		return err
	}
	_, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "platform_clearing", Currency: currency, AmountMinor: -amount})
	return err
}
