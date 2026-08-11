package commercepg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/gifts"
)

// GiftOrderInput describes one gift purchase.
//
// Exactly one payload is meaningful, and it is the one the kind names. The
// database enforces that with three CHECK constraints rather than trusting this
// struct, because a gift with two payloads is a gift nobody can price.
type GiftOrderInput struct {
	SenderID string
	Kind     string
	// PlanVersionID is set for a subscription gift, AddonVersionID for an
	// add-on, CreditMinor for wallet credit.
	PlanVersionID  string
	AddonVersionID string
	CreditMinor    int64
	Currency       string
	// RecipientTelegramID names an intended recipient. Zero leaves the gift
	// claimable by whoever holds the code, which is what makes a shareable gift
	// link work at all.
	RecipientTelegramID int64
	SenderMessage       string
	Lifetime            time.Duration
	IdempotencyKey      string
	SkipWallet          bool
}

// GiftPurchase is the result of opening a gift order.
//
// `Code` is the only time the plaintext claim code exists outside the sender's
// message. It is not stored: only its SHA-256 is, so a database read — a backup,
// a dump, a compromised replica — never yields a redeemable code. An operator
// who loses the code cannot recover it, which is the intended trade.
type GiftPurchase struct {
	Order dbgen.Order
	Gift  dbgen.Gift
	Code  string
}

var (
	// ErrGiftNotFound is returned for a code that matches nothing. It is
	// deliberately the same error a revoked or expired gift produces at the
	// transport layer, so a caller cannot use the difference to probe which
	// codes exist.
	ErrGiftNotFound = errors.New("gift code is not claimable")
	// ErrGiftOwnClaim is returned when a sender tries to claim their own gift.
	ErrGiftOwnClaim = errors.New("a gift cannot be claimed by its sender")
)

// CreateGiftOrder opens the sender's order and mints the claim code.
//
// The order stays in the sender's history and is never copied into the
// recipient's: `orders.user_id` is the sender, and what the claim produces —
// an entitlement or a ledger entry — carries the recipient. That is what keeps
// a gift out of the recipient's payment history while still giving them what
// was bought.
func (store *Store) CreateGiftOrder(
	ctx context.Context, input GiftOrderInput,
) (GiftPurchase, error) {
	senderID, err := parseUUID(input.SenderID)
	if err != nil {
		return GiftPurchase{}, fmt.Errorf("sender ID: %w", err)
	}
	amountMinor, err := store.giftAmount(ctx, input)
	if err != nil {
		return GiftPurchase{}, err
	}
	lifetime := input.Lifetime
	if lifetime <= 0 {
		lifetime = gifts.DefaultLifetime
	}

	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return GiftPurchase{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)

	if existing, existingErr := queries.GetOrderByIdempotency(ctx, dbgen.GetOrderByIdempotencyParams{
		UserID: senderID, IdempotencyKey: input.IdempotencyKey,
	}); existingErr == nil {
		// A replayed confirmation reaches the order that already exists. The
		// code is not reissued: it was shown once and only its hash was kept,
		// so there is nothing to show again.
		gift, giftErr := queries.GetGiftByOrder(ctx, existing.ID)
		if giftErr != nil {
			return GiftPurchase{}, giftErr
		}
		return GiftPurchase{Order: existing, Gift: gift}, tx.Commit(ctx)
	} else if !errors.Is(existingErr, pgx.ErrNoRows) {
		return GiftPurchase{}, existingErr
	}
	if err = store.assertOperational(ctx, queries); err != nil {
		return GiftPurchase{}, err
	}

	walletMinor := int64(0)
	if !input.SkipWallet {
		if walletMinor, err = store.walletContribution(
			ctx, queries, senderID, input.Currency, amountMinor,
		); err != nil {
			return GiftPurchase{}, err
		}
	}
	externalMinor := amountMinor - walletMinor
	state := string(commerce.OrderPending)
	if externalMinor == 0 {
		state = string(commerce.OrderPaid)
	}

	order, err := queries.CreateOrder(ctx, dbgen.CreateOrderParams{
		UserID: senderID, State: state, Operation: "gift", Currency: input.Currency,
		SubtotalMinor: amountMinor, DiscountMinor: 0,
		WalletMinor: walletMinor, ExternalMinor: externalMinor,
		IdempotencyKey:   input.IdempotencyKey,
		ExpiresAt:        pgtype.Timestamptz{Time: store.clock().Add(time.Hour), Valid: true},
		SelectedSquadIds: noSquads(),
	})
	if err != nil {
		return GiftPurchase{}, err
	}

	code, hint, err := gifts.NewCode()
	if err != nil {
		return GiftPurchase{}, err
	}
	gift, err := queries.CreateGift(ctx, dbgen.CreateGiftParams{
		OrderID: order.ID, SenderUserID: senderID, Kind: input.Kind,
		PlanVersionID:       nullableUUID(input.PlanVersionID),
		AddonVersionID:      nullableUUID(input.AddonVersionID),
		CreditMinor:         optionalInt8(input.CreditMinor),
		Currency:            input.Currency,
		CodeHash:            gifts.Hash(code),
		CodeHint:            hint,
		RecipientTelegramID: optionalInt8(input.RecipientTelegramID),
		SenderMessage:       optionalText(strings.TrimSpace(input.SenderMessage)),
		Lifetime:            giftInterval(lifetime),
	})
	if err != nil {
		return GiftPurchase{}, err
	}
	if state == string(commerce.OrderPaid) {
		if err = store.settleGiftOrder(ctx, queries, order, "wallet:"+input.IdempotencyKey); err != nil {
			return GiftPurchase{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return GiftPurchase{}, err
	}
	return GiftPurchase{Order: order, Gift: gift, Code: code}, nil
}

// giftAmount prices the gift from the catalog rather than from the caller.
//
// A wallet-credit gift is worth exactly what it credits; a plan or add-on gift
// is worth what that version costs today. Taking the number from the request
// would let a client name its own price.
func (store *Store) giftAmount(ctx context.Context, input GiftOrderInput) (int64, error) {
	queries := dbgen.New(store.pool)
	switch input.Kind {
	case "wallet_credit":
		if input.CreditMinor <= 0 {
			return 0, errors.New("a wallet-credit gift needs a positive amount")
		}
		return input.CreditMinor, nil
	case "subscription":
		versionID, err := parseUUID(input.PlanVersionID)
		if err != nil {
			return 0, fmt.Errorf("plan version ID: %w", err)
		}
		version, err := queries.GetPlanVersionForOrder(ctx, dbgen.GetPlanVersionForOrderParams{
			PlanVersionID: versionID, Currency: input.Currency,
		})
		if err != nil {
			return 0, err
		}
		return version.AmountMinor, nil
	case "addon":
		versionID, err := parseUUID(input.AddonVersionID)
		if err != nil {
			return 0, fmt.Errorf("add-on version ID: %w", err)
		}
		version, err := queries.GetAddonVersionForOrder(ctx, dbgen.GetAddonVersionForOrderParams{
			AddonVersionID: versionID, Currency: input.Currency,
		})
		if err != nil {
			return 0, err
		}
		return version.AmountMinor, nil
	default:
		return 0, errors.New("unsupported gift kind")
	}
}

// settleGiftOrder makes a paid gift claimable.
//
// The restriction to `pending` in `MarkGiftDeliverable` is what makes a
// replayed settlement a no-op: a webhook, a reconciliation poll, and a manual
// approval all converge on one deliverable gift rather than three.
func (store *Store) settleGiftOrder(
	ctx context.Context, queries *dbgen.Queries, order dbgen.Order, correlationID string,
) error {
	if err := store.recordOrderRevenue(ctx, queries, order, correlationID); err != nil {
		return err
	}
	gift, err := queries.GetGiftByOrder(ctx, order.ID)
	if err != nil {
		return err
	}
	if _, err = queries.MarkGiftDeliverable(ctx, gift.ID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return nil
}

// ClaimGift redeems a code for the claiming customer.
//
// The plaintext code is hashed and never stored or logged. The row is locked
// before anything is decided, so two simultaneous claims of one code serialise:
// the second observes a gift that is no longer deliverable and is refused,
// which is what makes single redemption a property of the schema rather than of
// timing.
func (store *Store) ClaimGift(
	ctx context.Context, code, claimantID string,
) (dbgen.Gift, error) {
	recipientID, err := parseUUID(claimantID)
	if err != nil {
		return dbgen.Gift{}, err
	}
	normalized, err := gifts.Normalize(code)
	if err != nil {
		return dbgen.Gift{}, ErrGiftNotFound
	}
	digest := gifts.Hash(normalized)

	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return dbgen.Gift{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)

	found, err := queries.GetGiftByCodeHash(ctx, digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.Gift{}, ErrGiftNotFound
	}
	if err != nil {
		return dbgen.Gift{}, err
	}
	gift, err := queries.LockGift(ctx, found.ID)
	if err != nil {
		return dbgen.Gift{}, err
	}
	if gift.SenderUserID == recipientID {
		// Claiming your own gift would launder an order into an entitlement
		// while leaving the gift looking redeemed by somebody else.
		_, _ = queries.RecordGiftClaimAttempt(ctx, gift.ID)
		return dbgen.Gift{}, ErrGiftOwnClaim
	}
	if gift.Status != "deliverable" || !gift.ExpiresAt.Time.After(store.clock().UTC()) {
		// Counting the attempt is the durable half of brute-force defence; the
		// rate limiter in front of the transport is the first half.
		_, _ = queries.RecordGiftClaimAttempt(ctx, gift.ID)
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return dbgen.Gift{}, commitErr
		}
		return dbgen.Gift{}, ErrGiftNotFound
	}

	var (
		ledgerID      pgtype.UUID
		entitlementID pgtype.UUID
	)
	switch gift.Kind {
	case gifts.KindCredit:
		if ledgerID, err = store.creditGift(ctx, queries, gift, recipientID); err != nil {
			return dbgen.Gift{}, err
		}
	case gifts.KindSubscription:
		if entitlementID, err = store.grantGiftSubscription(ctx, tx, queries, gift, recipientID); err != nil {
			return dbgen.Gift{}, err
		}
	default:
		// An add-on gift attaches to a subscription the recipient may not have.
		// Rather than guess which one, it is credited as wallet value the
		// recipient spends on the add-on themselves — which is also the only
		// answer that stays correct when they hold several subscriptions.
		if ledgerID, err = store.creditGift(ctx, queries, gift, recipientID); err != nil {
			return dbgen.Gift{}, err
		}
	}
	claimed, err := queries.ClaimGift(ctx, dbgen.ClaimGiftParams{
		GiftID: gift.ID, RecipientUserID: recipientID,
		ClaimEntitlementID:       entitlementID,
		ClaimLedgerTransactionID: ledgerID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Somebody else claimed it between the lock and here. Impossible with
		// the lock held, but refusing is the correct answer if it ever is.
		return dbgen.Gift{}, ErrGiftNotFound
	}
	if err != nil {
		return dbgen.Gift{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return dbgen.Gift{}, err
	}
	return claimed, nil
}

// grantGiftSubscription provisions the gifted plan for the recipient.
//
// The entitlement and its fulfillment operation are written in the claim's own
// transaction, so the gift cannot be marked claimed without the recipient
// actually getting something. Provisioning itself is the worker's job, pushed
// through the same pipeline a purchase uses: a gift is not a special kind of
// subscription and must not have a special kind of provisioning.
func (store *Store) grantGiftSubscription(
	ctx context.Context, tx pgx.Tx, queries *dbgen.Queries,
	gift dbgen.Gift, recipientID pgtype.UUID,
) (pgtype.UUID, error) {
	plan, err := queries.GetPlanVersionForOrder(ctx, dbgen.GetPlanVersionForOrderParams{
		PlanVersionID: gift.PlanVersionID, Currency: gift.Currency,
	})
	if err != nil {
		return pgtype.UUID{}, err
	}
	subscriptionID, err := store.resolveSubscription(
		ctx, queries, recipientID, plan, CreateOrderInput{Operation: "purchase"},
	)
	if err != nil {
		return pgtype.UUID{}, err
	}
	now := store.clock().UTC()
	var currentEndsAt *time.Time
	if current, currentErr := queries.GetLatestEntitlementForChange(ctx,
		dbgen.GetLatestEntitlementForChangeParams{UserID: recipientID, SubscriptionID: subscriptionID},
	); currentErr == nil {
		currentEndsAt = &current.EndsAt.Time
	} else if !errors.Is(currentErr, pgx.ErrNoRows) {
		return pgtype.UUID{}, currentErr
	}
	// A gift extends what the recipient already has rather than replacing it.
	// Replacing would mean a present that shortens somebody's subscription.
	operation := "purchase"
	if currentEndsAt != nil && currentEndsAt.After(now) {
		operation = "extension"
	}
	schedule, err := commerce.ScheduleEntitlement(
		now, time.Duration(plan.DurationSeconds)*time.Second, operation,
		plan.UpgradePolicy, plan.DowngradePolicy, currentEndsAt,
	)
	if err != nil {
		return pgtype.UUID{}, err
	}
	entitlement, err := queries.CreateEntitlement(ctx, dbgen.CreateEntitlementParams{
		UserID: recipientID, OrderID: gift.OrderID, PlanVersionID: plan.ID,
		StartsAt:              pgtype.Timestamptz{Time: schedule.StartsAt, Valid: true},
		EndsAt:                pgtype.Timestamptz{Time: schedule.EndsAt, Valid: true},
		TrafficAllowanceBytes: plan.TrafficAllowanceBytes,
		DeviceLimit:           plan.DeviceLimit,
		RemnawaveSquadIds:     plan.RemnawaveSquadIds,
		SubscriptionID:        subscriptionID,
	})
	if err != nil {
		return pgtype.UUID{}, err
	}
	desired, _ := json.Marshal(map[string]any{
		"effectiveAt": schedule.EffectiveAt, "endsAt": schedule.EndsAt,
		"trafficAllowanceBytes": nullableInt8(entitlement.TrafficAllowanceBytes),
		"deviceLimit":           nullableInt4(entitlement.DeviceLimit),
		"squadIds":              databaseutil.UUIDStrings(entitlement.RemnawaveSquadIds),
	})
	operationRow, err := queries.CreateFulfillmentOperation(ctx, dbgen.CreateFulfillmentOperationParams{
		EntitlementID: entitlement.ID, Operation: "create",
		IdempotencyKey: "gift:" + uuidString(gift.ID) + ":fulfill",
		CorrelationID:  "gift:" + uuidString(gift.ID),
		DesiredState:   desired,
	})
	if err != nil {
		return pgtype.UUID{}, err
	}
	// The job is inserted in this transaction, so provisioning is queued if and
	// only if the claim commits.
	if store.enqueue != nil {
		if err = store.enqueue(ctx, tx, uuidString(operationRow.ID)); err != nil {
			return pgtype.UUID{}, err
		}
	}
	return entitlement.ID, nil
}

// creditGift moves a wallet-credit gift into the recipient's balance.
//
// The ledger transaction is keyed on the gift, so the double-entry pair is
// written once however many times a claim is attempted.
func (store *Store) creditGift(
	ctx context.Context, queries *dbgen.Queries, gift dbgen.Gift, recipientID pgtype.UUID,
) (pgtype.UUID, error) {
	amount := gift.CreditMinor.Int64
	entries := []commerce.LedgerEntry{
		{AccountType: "customer_wallet", CustomerID: uuidString(recipientID), Currency: gift.Currency, AmountMinor: amount},
		{AccountType: "revenue", Currency: gift.Currency, AmountMinor: -amount},
	}
	if err := commerce.ValidateLedger(entries); err != nil {
		return pgtype.UUID{}, err
	}
	transaction, err := queries.CreateLedgerTransaction(ctx, dbgen.CreateLedgerTransactionParams{
		Type: "credit", ReferenceType: "gift", ReferenceID: uuidString(gift.ID),
		IdempotencyKey: "gift:" + uuidString(gift.ID),
		Reason:         pgtype.Text{String: "gift claim", Valid: true},
	})
	if err != nil {
		return pgtype.UUID{}, err
	}
	if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{
		TransactionID: transaction.ID, AccountType: "customer_wallet",
		UserID: recipientID, Currency: gift.Currency, AmountMinor: amount,
	}); err != nil {
		return pgtype.UUID{}, err
	}
	if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{
		TransactionID: transaction.ID, AccountType: "revenue",
		Currency: gift.Currency, AmountMinor: -amount,
	}); err != nil {
		return pgtype.UUID{}, err
	}
	return transaction.ID, nil
}

// optionalInt8 renders a zero as absent, which is what every nullable count and
// amount in the gift schema means by NULL.
func optionalInt8(value int64) pgtype.Int8 {
	if value == 0 {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: value, Valid: true}
}

// nullableUUID renders an empty string as SQL NULL, which is what an absent
// gift payload means.
func nullableUUID(value string) pgtype.UUID {
	if value == "" {
		return pgtype.UUID{}
	}
	parsed, err := parseUUID(value)
	if err != nil {
		return pgtype.UUID{}
	}
	return parsed
}

// giftInterval converts a lifetime into the interval the insert adds to now().
func giftInterval(duration time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: duration.Microseconds(), Valid: true}
}
