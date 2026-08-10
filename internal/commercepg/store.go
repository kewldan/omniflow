package commercepg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/commerce"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

type EnqueueFulfillment func(context.Context, pgx.Tx, string) error

type Store struct {
	pool    *pgxpool.Pool
	enqueue EnqueueFulfillment
	clock   func() time.Time
}

func New(pool *pgxpool.Pool, enqueue EnqueueFulfillment) *Store {
	return &Store{pool: pool, enqueue: enqueue, clock: time.Now}
}

func (store *Store) RunMaintenance(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		_, _ = dbgen.New(store.pool).ExpirePendingOrders(ctx)
		store.expireWalletCredits(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
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
	ExpiresAt      time.Time
	// SkipWallet keeps the customer's wallet balance out of this order. The
	// zero value applies the wallet first, which is the documented default.
	SkipWallet bool
}

// Promotion and policy rejections carry a stable machine reason so a customer
// surface can explain exactly why a code was refused.
var (
	ErrPromoUnknown        = errors.New("promo_unknown")
	ErrPromoIneligible     = errors.New("promo_ineligible")
	ErrPromoExhausted      = errors.New("promo_exhausted")
	ErrPromoInvalid        = errors.New("promo_invalid")
	ErrOperationForbidden  = errors.New("operation_forbidden")
	ErrPlanUnavailable     = errors.New("plan_unavailable")
	ErrTrialAlreadyClaimed = errors.New("trial_already_used")
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
	promo         *dbgen.GetPromoForRedemptionRow
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
		if eligibilityErr != nil || !eligible.Valid || !eligible.Bool {
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
		promotion := commerce.Promotion{Kind: lockedPromo.Kind, Value: lockedPromo.Value, CustomerLimit: int(lockedPromo.PerCustomerLimit), RedemptionCount: int(counts.TotalCount), CustomerRedeemed: int(counts.CustomerCount)}
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
	applicableWallet := wallet
	if input.SkipWallet {
		applicableWallet = 0
	}
	domainOrder, err := commerce.NewOrder("", input.CustomerID, commerce.Money{Amount: plan.AmountMinor, Currency: plan.Currency}, discount, applicableWallet)
	if err != nil {
		return OrderQuote{}, err
	}
	return OrderQuote{Plan: plan, DiscountMinor: discount, WalletBalance: wallet, WalletMinor: domainOrder.WalletMinor, ExternalMinor: domainOrder.ExternalMinor, promo: promo}, nil
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
	quote, err := store.quote(ctx, queries, userID, planVersionID, input)
	if err != nil {
		return dbgen.Order{}, err
	}
	plan, discount, promo := quote.Plan, quote.DiscountMinor, quote.promo
	domainOrder := commerce.Order{WalletMinor: quote.WalletMinor, ExternalMinor: quote.ExternalMinor}
	state := string(commerce.OrderPending)
	if domainOrder.ExternalMinor == 0 {
		state = string(commerce.OrderPaid)
	}
	expiresAt := input.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = store.clock().Add(time.Hour)
	}
	order, err := queries.CreateOrder(ctx, dbgen.CreateOrderParams{
		UserID: userID, State: state, Operation: input.Operation, Currency: plan.Currency,
		SubtotalMinor: plan.AmountMinor, DiscountMinor: discount, WalletMinor: domainOrder.WalletMinor,
		ExternalMinor: domainOrder.ExternalMinor, IdempotencyKey: input.IdempotencyKey,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if err != nil {
		return dbgen.Order{}, err
	}
	snapshot, _ := json.Marshal(map[string]any{
		"planCode": plan.Code, "planKind": plan.Kind, "version": plan.Version,
		"billingPeriod": plan.BillingPeriod, "durationSeconds": plan.DurationSeconds,
		"trafficAllowanceBytes": nullableInt8(plan.TrafficAllowanceBytes),
		"deviceLimit":           nullableInt4(plan.DeviceLimit), "squadIds": databaseutil.UUIDStrings(plan.RemnawaveSquadIds),
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
		claim, claimErr := tx.Exec(ctx, `INSERT INTO trial_claims (user_id, plan_id, order_id)
			VALUES ($1, $2, $3) ON CONFLICT (user_id) DO NOTHING`, userID, plan.PlanID, order.ID)
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
	if applyErr == nil && (classification == "paid" || classification == "late") && order.WalletMinor > 0 {
		walletBalance, balanceErr := queries.GetWalletBalance(ctx, dbgen.GetWalletBalanceParams{UserID: order.UserID, Currency: order.Currency})
		if balanceErr != nil {
			return dbgen.Order{}, "", balanceErr
		}
		if walletBalance < order.WalletMinor {
			classification = "wallet_unavailable"
		}
	}
	eventType := classification
	if eventType == "paid" {
		eventType = "status_changed"
	} else if eventType == "currency_mismatch" {
		eventType = "amount_mismatch"
	}
	if _, err = queries.InsertPaymentEvent(ctx, dbgen.InsertPaymentEventParams{PaymentIntentID: intent.ID, Type: eventType, PreviousStatus: pgtype.Text{String: intent.Status, Valid: true}, Status: pgtype.Text{String: "succeeded", Valid: true}, AmountMinor: pgtype.Int8{Int64: amount.Amount, Valid: true}, Currency: pgtype.Text{String: amount.Currency, Valid: true}, ProviderEventID: pgtype.Text{String: providerEventID, Valid: true}, Details: []byte(`{}`)}); err != nil {
		return dbgen.Order{}, "", err
	}
	if applyErr != nil || classification == "underpayment" || classification == "overpayment" || classification == "currency_mismatch" || classification == "wallet_unavailable" || classification == "duplicate" {
		if err = tx.Commit(ctx); err != nil {
			return dbgen.Order{}, "", err
		}
		return order, classification, applyErr
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
	return order, classification, nil
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
	if _, err = queries.InsertAuditEvent(ctx, dbgen.InsertAuditEventParams{ActorType: "operator", ActorID: pgtype.Text{String: actorID, Valid: true}, Action: "wallet.adjusted", TargetType: "customer", TargetID: customerID, Reason: pgtype.Text{String: reason, Valid: true}, Metadata: metadata}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (store *Store) settlePaidOrder(ctx context.Context, tx pgx.Tx, queries *dbgen.Queries, order dbgen.Order, idempotencyKey string) error {
	settledMinor := order.WalletMinor + order.ExternalMinor
	if settledMinor > 0 {
		transaction, err := queries.CreateLedgerTransaction(ctx, dbgen.CreateLedgerTransactionParams{Type: "payment", ReferenceType: "order", ReferenceID: uuidString(order.ID), IdempotencyKey: idempotencyKey})
		if err != nil {
			return err
		}
		if order.WalletMinor > 0 {
			if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "customer_wallet", UserID: order.UserID, Currency: order.Currency, AmountMinor: -order.WalletMinor}); err != nil {
				return err
			}
		}
		if order.ExternalMinor > 0 {
			if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "provider_clearing", Currency: order.Currency, AmountMinor: -order.ExternalMinor}); err != nil {
				return err
			}
		}
		if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "platform_clearing", Currency: order.Currency, AmountMinor: settledMinor}); err != nil {
			return err
		}
	}
	spec, err := queries.GetOrderEntitlementSpec(ctx, order.ID)
	if err != nil {
		return err
	}
	startsAt := store.clock().UTC()
	var currentEndsAt *time.Time
	if current, currentErr := queries.GetLatestEntitlementForChange(ctx, order.UserID); currentErr == nil {
		currentEndsAt = &current.EndsAt.Time
	} else if !errors.Is(currentErr, pgx.ErrNoRows) {
		return currentErr
	}
	schedule, err := commerce.ScheduleEntitlement(startsAt, time.Duration(spec.DurationSeconds)*time.Second, order.Operation, spec.UpgradePolicy, spec.DowngradePolicy, currentEndsAt)
	if err != nil {
		return err
	}
	entitlement, err := queries.CreateEntitlement(ctx, dbgen.CreateEntitlementParams{UserID: order.UserID, OrderID: order.ID, PlanVersionID: spec.PlanVersionID, StartsAt: pgtype.Timestamptz{Time: schedule.StartsAt, Valid: true}, EndsAt: pgtype.Timestamptz{Time: schedule.EndsAt, Valid: true}, TrafficAllowanceBytes: spec.TrafficAllowanceBytes, DeviceLimit: spec.DeviceLimit, RemnawaveSquadIds: spec.RemnawaveSquadIds})
	if err != nil {
		return err
	}
	desired, _ := json.Marshal(map[string]any{"effectiveAt": schedule.EffectiveAt, "endsAt": schedule.EndsAt, "trafficAllowanceBytes": nullableInt8(spec.TrafficAllowanceBytes), "deviceLimit": nullableInt4(spec.DeviceLimit), "squadIds": databaseutil.UUIDStrings(spec.RemnawaveSquadIds)})
	operation, err := queries.CreateFulfillmentOperation(ctx, dbgen.CreateFulfillmentOperationParams{EntitlementID: entitlement.ID, Operation: "create", IdempotencyKey: "order:" + uuidString(order.ID) + ":fulfill", CorrelationID: idempotencyKey, DesiredState: desired})
	if err != nil {
		return err
	}
	if _, err = queries.InsertOutboxEvent(ctx, dbgen.InsertOutboxEventParams{Topic: "order.paid", Payload: []byte(fmt.Sprintf(`{"orderId":%q,"entitlementId":%q}`, uuidString(order.ID), uuidString(entitlement.ID)))}); err != nil {
		return err
	}
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
